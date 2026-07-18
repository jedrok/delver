package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jedrok/delver/internal/activities"
	"github.com/jedrok/delver/internal/config"
	"github.com/jedrok/delver/internal/types"
	"github.com/jedrok/delver/internal/workflows"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/workflow"
)

// top-level orchestrator
// phase 1 break question into sub questions
// phase 2 research each sub question in parallel using child workflows
// phase 3 synthesize all findings into a single cited report
// phase 4 give human approval for the research pipeline
func ResearchPipelineWorkflow(
	ctx workflow.Context,
	input types.PipelineInput,
) (types.PipelineOutput, error) {

	logger := workflow.GetLogger(ctx)
	logger.Info("pipeline started", "question", input.Question)

	llmCtx := workflow.WithActivityOptions(ctx, config.LLMCallOptions)
	var llmActivities *activities.LLMActivities

	// phase 1 planning
	logger.Info("phase 1: planning")

	var plan types.PlanOutput

	planMessages := []types.AgentMessage{
		{
			Role: "user",
			Content: fmt.Sprintf(`Return ONLY valid JSON in this exact format: {"sub_questions": ["question 1","question 2"]}
				Do not include markdown. Do not include explanations. Do not include code fences. Question: %s`, input.Question),
		},
	}

	var planResp types.LLMCallOutput

	err := workflow.ExecuteActivity(llmCtx, llmActivities.GenericLLMCall, types.LLMCallInput{
		Model:    input.PlanModel,
		Messages: planMessages}).Get(ctx, &planResp)

	if err != nil {
		return types.PipelineOutput{},
			fmt.Errorf("planning phase failed: %w", err)
	}

	// parse json response
	plan, err = parsePlanResponse(planResp.Content)
	if err != nil {
		return types.PipelineOutput{},
			fmt.Errorf("planning response invalid: %w", err)
	}

	logger.Info("plan complete", "sub_questions", len(plan.SubQuestions))

	// phase 2 research in parellel
	logger.Info("phase 2: researching", "count", len(plan.SubQuestions))

	results := make([]types.ResearchResult, len(plan.SubQuestions))
	errs := make([]error, len(plan.SubQuestions))

	wg := workflow.NewWaitGroup(ctx)

	for i, q := range plan.SubQuestions {
		// i, q := i, q

		// stagger the child workflows by a few secs to avoid hitting api alot
		if i > 0 {
			if err := workflow.Sleep(ctx, time.Second*12); err != nil {
				return types.PipelineOutput{}, err
			}
		}

		wg.Add(1)

		workflow.Go(ctx, func(gCtx workflow.Context) {
			defer wg.Done()

			childCtx := workflow.WithChildOptions(
				gCtx,
				workflow.ChildWorkflowOptions{
					WorkflowID: fmt.Sprintf(
						"%s-research-%d",
						workflow.GetInfo(ctx).WorkflowExecution.ID,
						i,
					),
					ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_TERMINATE,
				},
			)

			var result types.ResearchResult

			cErr := workflow.ExecuteChildWorkflow(
				childCtx,
				workflows.AgentLoopWorkflow,
				types.AgentLoopInput{
					Task:          q,
					MaxIterations: 15,
					Model:         input.ResearchModel,
				},
			).Get(gCtx, &result)

			results[i] = result
			errs[i] = cErr
		})
	}

	wg.Wait(ctx)

	// collect partial results/don't fail the whole pipeline if some sub quests failed
	var goodResults []types.ResearchResult

	for i, r := range results {
		if errs[i] == nil {
			goodResults = append(goodResults, r)
		} else {
			logger.Warn(
				"sub-question research failed",
				"question",
				plan.SubQuestions[i],
				"error",
				errs[i],
			)
		}
	}

	if len(goodResults) == 0 {
		return types.PipelineOutput{},
			fmt.Errorf("all research sub-tasks failed")
	}

	logger.Info("research complete", "succeeded", len(goodResults), "failed", len(plan.SubQuestions)-len(goodResults))

	// phase 3 synthesize
	logger.Info("phase 3: synthesizing")

	synthesisMessages := buildSynthesisPrompt(
		input.Question,
		goodResults,
	)

	var synthesisResp types.LLMCallOutput

	err = workflow.ExecuteActivity(
		llmCtx,
		llmActivities.GenericLLMCall,
		types.LLMCallInput{
			Model:    input.SynthesisModel,
			Messages: synthesisMessages,
		},
	).Get(ctx, &synthesisResp)

	if err != nil {
		return types.PipelineOutput{},
			fmt.Errorf("synthesis phase failed: %w", err)
	}

	if !input.RequireApproval {
		logger.Info("pipeline complete", "status", "approved")
		return types.PipelineOutput{
			Report: synthesisResp.Content,
			Status: "approved",
		}, nil
	}

	logger.Info("phase 4: waiting for approval")

	timeout := input.ApprovalTimeout
	if timeout == 0 {
		timeout = 24 * time.Hour
	}

	var approvalResult types.ApprovalResult
	approvalCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: fmt.Sprintf("%s-approval",
			workflow.GetInfo(ctx).WorkflowExecution.ID),
		ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_TERMINATE,
	})
	err = workflow.ExecuteChildWorkflow(approvalCtx, workflows.ApprovalGateWorkflow,
		types.ApprovalGateInput{
			Report:  synthesisResp.Content,
			Timeout: timeout,
		},
	).Get(ctx, &approvalResult)
	if err != nil {
		return types.PipelineOutput{}, fmt.Errorf("approval gate failed: %w", err)
	}

	logger.Info("pipeline complete", "status", approvalResult.Status)
	if approvalResult.Status != "approved" {
		return types.PipelineOutput{Status: approvalResult.Status}, nil
	}
	return types.PipelineOutput{
		Report: approvalResult.Report,
		Status: approvalResult.Status,
	}, nil
}

func parsePlanResponse(content string) (types.PlanOutput, error) {
	var plan types.PlanOutput

	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return types.PlanOutput{},
			fmt.Errorf("failed to parse planner JSON: %w", err)
	}

	if len(plan.SubQuestions) == 0 {
		return types.PlanOutput{},
			fmt.Errorf("planner returned zero sub-questions")
	}

	return plan, nil
}

// put together the final synthesis prompt from all research results
func buildSynthesisPrompt(
	question string,
	results []types.ResearchResult,
) []types.AgentMessage {
	var b strings.Builder
	fmt.Fprintf(&b, "Synthesize a cited report answering: %s\n\nResearch findings:\n", question)

	for _, r := range results {
		fmt.Fprintf(&b, "\n--- Sub-question: %s ---\n", r.SubQuestion)
		for _, f := range r.Findings {
			if f.Role == "tool" || f.Role == "assistant" {
				b.WriteString(f.Content)
				b.WriteByte('\n')
			}
		}
	}

	return []types.AgentMessage{
		{
			Role:    "user",
			Content: b.String(),
		},
	}
}
