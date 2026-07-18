package workflows

import (
	"fmt"
	"time"

	"github.com/jedrok/delver/internal/activities"
	"github.com/jedrok/delver/internal/config"
	"github.com/jedrok/delver/internal/types"
	"go.temporal.io/sdk/workflow"
)

func AgentLoopWorkflow(ctx workflow.Context, input types.AgentLoopInput) (types.ResearchResult, error) {

	logger := workflow.GetLogger(ctx)
	logger.Info("agent loop started", "task: ", input.Task)

	// set up temporal activity proxies
	llmCtx := workflow.WithActivityOptions(ctx, config.LLMCallOptions)

	toolCtx := workflow.WithActivityOptions(ctx, config.ToolCallOptions)

	var llmActivities *activities.LLMActivities
	var toolActivities *activities.ToolActivities

	//start conversation with the task as the first message
	history := []types.AgentMessage{
		{Role: "user", Content: fmt.Sprintf("Research the following topic thoroughly: %s", input.Task)},
	}

	var iterations int = 0
	for iterations < input.MaxIterations {
		logger.Info("agent loop iteration", "iteration", iterations+1, "max", input.MaxIterations)
		// first step. ask llm what to do next
		var decision types.LLMCallOutput
		err := workflow.ExecuteActivity(llmCtx, llmActivities.GenericLLMCall, types.LLMCallInput{
			Model:    input.Model,
			Messages: history,
			Tools:    input.ToolDef,
		}).Get(ctx, &decision)

		if err != nil {
			logger.Warn("LLM call failed after retries", "error", err)
			return types.ResearchResult{}, err
		}

		// add the llm response to history
		history = append(history, types.AgentMessage{
			Role:      "assistant",
			Content:   decision.Content,
			ToolCalls: decision.ToolCalls,
		})
		// check if the llm is done
		if decision.Done || len(decision.ToolCalls) == 0 {
			logger.Info("agent loop complete", "iterations", iterations+1)
			break
		}

		for _, call := range decision.ToolCalls {
			var toolResult types.ToolCallOutput
			err = workflow.ExecuteActivity(toolCtx,
				toolActivities.DispatchTool,
				types.ToolCallInput{
					ToolName: call.Name,
					ToolArgs: call.Args,
					CallID:   call.ID,
				},
			).Get(ctx, &toolResult)
			if err != nil {
				logger.Warn("tool call failed", "tool", call.Name, "error", err)
				history = append(history, types.AgentMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("tool %s failed: %v", call.Name, err),
				})
				continue
			}
			history = append(history, types.AgentMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    toolResult.Result,
			})
		}

		iterations++

		// small pause
		_ = workflow.Sleep(ctx, 500*time.Millisecond)
	}

	if iterations >= input.MaxIterations {
		logger.Warn("agent loop hit max iterations", "max", input.MaxIterations)
	}

	return types.ResearchResult{
		SubQuestion: input.Task,
		Findings:    history,
	}, nil

}

const ApprovalSignaleName string = "approval"

func ApprovalGateWorkflow(ctx workflow.Context, input types.ApprovalGateInput) (types.ApprovalResult, error) {

	logger := workflow.GetLogger(ctx)
	logger.Info("approval gate is open, waiting for decision")

	currentStatus := types.PipelineStatus{
		Phase:  "approval",
		Status: "waiting",
	}
	err := workflow.SetQueryHandler(ctx, "getStatus",
		func() (types.PipelineStatus, error) {
			return currentStatus, nil
		})
	if err != nil {
		return types.ApprovalResult{}, err
	}

	signalChan := workflow.GetSignalChannel(ctx, ApprovalSignaleName)
	selector := workflow.NewSelector(ctx)

	var decision types.ApprovalDecision
	received := false

	selector.AddReceive(signalChan, func(c workflow.ReceiveChannel, more bool) {
		c.Receive(ctx, &decision)
		received = true
	})

	timerFuture := workflow.NewTimer(ctx, input.Timeout)
	selector.AddFuture(timerFuture, func(f workflow.Future) {
		logger.Info("approval gate timed out")
	})

	selector.Select(ctx)

	if !received {
		currentStatus.Status = "timed_out"
		return types.ApprovalResult{Status: "timed_out"}, nil
	}

	if decision.Action != "approve" {
		currentStatus.Status = "rejected"
		return types.ApprovalResult{Status: "rejected"}, nil
	}

	currentStatus.Status = "approved"
	return types.ApprovalResult{Status: "approved", Report: input.Report}, nil
}
