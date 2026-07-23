package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/jedrok/delver/internal/activities"
	"github.com/jedrok/delver/internal/types"
	"github.com/jedrok/delver/internal/workflows"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// activity refrence
var llmActs *activities.LLMActivities

func newPipelineEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ResearchPipelineWorkflow)
	env.RegisterWorkflow(workflows.AgentLoopWorkflow)
	env.RegisterWorkflow(workflows.ApprovalGateWorkflow)
	// stable parent id so approval child id is predictable
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{
		ID: "pipe-test",
	})
	return env
}

func basePipeline() types.PipelineInput {
	return types.PipelineInput{
		Question:        "what is the capital of usa",
		PlanModel:       "plan-m",
		ResearchModel:   "research-m",
		SynthesisModel:  "synth-m",
		RequireApproval: false,
	}
}

// plan llm returns json with one sub question, research answers once, synth writes report
func mockHappyPathLLM(env *testsuite.TestWorkflowEnvironment, report string) {
	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "plan-m"
	})).Return(types.LLMCallOutput{
		Content: `{"sub_questions":["what is the capital of usa"]}`,
		Done:    true,
	}, nil).Once()

	// agent loop research call (model and tools offered)
	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "research-m"
	})).Return(types.LLMCallOutput{
		Content: "washington dc is the capital",
		Done:    true,
	}, nil).Once()

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "synth-m"
	})).Return(types.LLMCallOutput{
		Content: report,
		Done:    true,
	}, nil).Once()
}

func TestPipelineHappyPathNoApproval(t *testing.T) {
	env := newPipelineEnv(t)
	report := "final report: washington dc"
	mockHappyPathLLM(env, report)

	env.ExecuteWorkflow(ResearchPipelineWorkflow, basePipeline())
	out := mustPipelineResult(t, env)

	if out.Status != "approved" {
		t.Errorf("status = %q, want approved", out.Status)
	}
	if out.Report != report {
		t.Errorf("report = %q, want %q", out.Report, report)
	}
}

func TestPipelineBadPlanJSON(t *testing.T) {
	env := newPipelineEnv(t)

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "plan-m"
	})).Return(types.LLMCallOutput{
		Content: "not json at all",
		Done:    true,
	}, nil).Once()

	env.ExecuteWorkflow(ResearchPipelineWorkflow, basePipeline())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("expected error for bad plan json")
	}
	if !strings.Contains(err.Error(), "planning response invalid") &&
		!strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("error = %q", err)
	}
}

func TestPipelineEmptySubQuestions(t *testing.T) {
	env := newPipelineEnv(t)

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "plan-m"
	})).Return(types.LLMCallOutput{
		Content: `{"sub_questions":[]}`,
		Done:    true,
	}, nil).Once()

	env.ExecuteWorkflow(ResearchPipelineWorkflow, basePipeline())

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("expected error for empty sub-questions")
	}
	if !strings.Contains(err.Error(), "planning response invalid") &&
		!strings.Contains(err.Error(), "zero sub-questions") {
		t.Errorf("error = %q", err)
	}
}

func TestPipelineAllResearchFails(t *testing.T) {
	env := newPipelineEnv(t)

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "plan-m"
	})).Return(types.LLMCallOutput{
		Content: `{"sub_questions":["hard question"]}`,
		Done:    true,
	}, nil).Once()

	// research agent llm dies permanently
	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "research-m"
	})).Return(
		types.LLMCallOutput{},
		temporal.NewNonRetryableApplicationError("research llm down", "PermanentError", nil),
	).Once()

	env.ExecuteWorkflow(ResearchPipelineWorkflow, basePipeline())

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("expected error when all research fails")
	}
	if !strings.Contains(err.Error(), "all research sub-tasks failed") {
		t.Errorf("error = %q", err)
	}
}

func TestPipelinePartialResearchFailure(t *testing.T) {
	// if one child fails and one succeeds we still synthesize
	env := newPipelineEnv(t)

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "plan-m"
	})).Return(types.LLMCallOutput{
		Content: `{"sub_questions":["good question","bad question"]}`,
		Done:    true,
	}, nil).Once()

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "research-m" && messagesContain(in.Messages, "good question")
	})).Return(types.LLMCallOutput{
		Content: "good findings",
		Done:    true,
	}, nil).Once()

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "research-m" && messagesContain(in.Messages, "bad question")
	})).Return(
		types.LLMCallOutput{},
		temporal.NewNonRetryableApplicationError("bad child", "PermanentError", nil),
	).Once()

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		return in.Model == "synth-m"
	})).Return(types.LLMCallOutput{
		Content: "partial report from good findings only",
		Done:    true,
	}, nil).Once()

	env.ExecuteWorkflow(ResearchPipelineWorkflow, basePipeline())
	out := mustPipelineResult(t, env)

	if out.Status != "approved" {
		t.Errorf("status = %q", out.Status)
	}
	if !strings.Contains(out.Report, "partial report") {
		t.Errorf("report = %q", out.Report)
	}
}

func TestPipelineApprovalApprove(t *testing.T) {
	env := newPipelineEnv(t)
	report := "needs human ok"
	mockHappyPathLLM(env, report)

	// after research and synth,signal the approval child
	env.RegisterDelayedCallback(func() {
		_ = env.SignalWorkflowByID(
			"pipe-test-approval",
			workflows.ApprovalSignaleName,
			types.ApprovalDecision{Action: "approve"},
		)
	}, time.Second)

	in := basePipeline()
	in.RequireApproval = true
	in.ApprovalTimeout = time.Hour

	env.ExecuteWorkflow(ResearchPipelineWorkflow, in)
	out := mustPipelineResult(t, env)

	if out.Status != "approved" {
		t.Errorf("status = %q, want approved", out.Status)
	}
	if out.Report != report {
		t.Errorf("report = %q", out.Report)
	}
}

func TestPipelineApprovalReject(t *testing.T) {
	env := newPipelineEnv(t)
	mockHappyPathLLM(env, "secret report")

	env.RegisterDelayedCallback(func() {
		_ = env.SignalWorkflowByID(
			"pipe-test-approval",
			workflows.ApprovalSignaleName,
			types.ApprovalDecision{Action: "reject"},
		)
	}, time.Second)

	in := basePipeline()
	in.RequireApproval = true
	in.ApprovalTimeout = time.Hour

	env.ExecuteWorkflow(ResearchPipelineWorkflow, in)
	out := mustPipelineResult(t, env)

	if out.Status != "rejected" {
		t.Errorf("status = %q, want rejected", out.Status)
	}
	if out.Report != "" {
		t.Errorf("report should be empty on reject, got %q", out.Report)
	}
}

func TestPipelineApprovalTimeout(t *testing.T) {
	// no signal. approval timer is skipped by the testsuite
	env := newPipelineEnv(t)
	mockHappyPathLLM(env, "late report")

	in := basePipeline()
	in.RequireApproval = true
	in.ApprovalTimeout = time.Hour

	env.ExecuteWorkflow(ResearchPipelineWorkflow, in)
	out := mustPipelineResult(t, env)

	if out.Status != "timed_out" {
		t.Errorf("status = %q, want timed_out", out.Status)
	}
	if out.Report != "" {
		t.Errorf("report should be empty on timeout, got %q", out.Report)
	}
}

func mustPipelineResult(t *testing.T, env *testsuite.TestWorkflowEnvironment) types.PipelineOutput {
	t.Helper()
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out types.PipelineOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("get result: %v", err)
	}
	return out
}

func messagesContain(msgs []types.AgentMessage, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}
