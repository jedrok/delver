package workflows

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jedrok/delver/internal/activities"
	"github.com/jedrok/delver/internal/types"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// activity method references
var (
	llmActs  *activities.LLMActivities
	toolActs *activities.ToolActivities
)

func newAgentEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(AgentLoopWorkflow)
	return env
}

func sampleTools() []types.ToolDef {
	return []types.ToolDef{
		{
			Name:        "web_search",
			Description: "search the web",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		},
	}
}

func baseInput() types.AgentLoopInput {
	return types.AgentLoopInput{
		Task:          "what is the capital of usa",
		MaxIterations: 5,
		Model:         "test-model",
		ToolDef:       sampleTools(),
	}
}

func TestAgentLoopDoneNoTools(t *testing.T) {
	env := newAgentEnv(t)

	// one llm reply
	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.Anything).Return(
		types.LLMCallOutput{
			Content: "the capital is washington dc",
			Done:    true,
		},
		nil,
	).Once()

	env.ExecuteWorkflow(AgentLoopWorkflow, baseInput())
	result := mustAgentResult(t, env)

	if result.SubQuestion != "what is the capital of usa" {
		t.Errorf("sub-question = %q", result.SubQuestion)
	}
	if !hasRole(result.Findings, "assistant") {
		t.Error("expected assistant message in findings")
	}
	// DispatchTool must not have been called
	env.AssertNotCalled(t, "DispatchTool")
}

func TestAgentLoopToolInvoked(t *testing.T) {
	// tools must be passed to the llm and DispatchTool must run
	env := newAgentEnv(t)

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		// first call must offer tools to the model
		return len(in.Tools) > 0 && in.Tools[0].Name == "web_search"
	})).Return(
		types.LLMCallOutput{
			Content: "",
			Done:    false,
			ToolCalls: []types.ToolCall{
				{
					ID:   "call-1",
					Name: "web_search",
					Args: json.RawMessage(`{"query":"capital of usa"}`),
				},
			},
		},
		nil,
	).Once()

	env.OnActivity(toolActs.DispatchTool, mock.Anything, mock.MatchedBy(func(in types.ToolCallInput) bool {
		return in.ToolName == "web_search" && in.CallID == "call-1"
	})).Return(
		types.ToolCallOutput{Result: "washington dc is the capital of usa"},
		nil,
	).Once()

	// second llm call.final answer after tool result is in history
	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.MatchedBy(func(in types.LLMCallInput) bool {
		// history should include the tool message from previous step
		return hasRole(in.Messages, "tool")
	})).Return(
		types.LLMCallOutput{
			Content: "washington dc",
			Done:    true,
		},
		nil,
	).Once()

	env.ExecuteWorkflow(AgentLoopWorkflow, baseInput())
	result := mustAgentResult(t, env)

	if !hasRole(result.Findings, "tool") {
		t.Error("expected tool result in findings")
	}
	if !hasContent(result.Findings, "washington dc") {
		t.Error("expected final answer in findings")
	}
}

func TestAgentLoopParallelToolCalls(t *testing.T) {
	env := newAgentEnv(t)

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.Anything).Return(
		types.LLMCallOutput{
			Done: false,
			ToolCalls: []types.ToolCall{
				{ID: "c1", Name: "web_search", Args: json.RawMessage(`{"query":"a"}`)},
				{ID: "c2", Name: "web_search", Args: json.RawMessage(`{"query":"b"}`)},
			},
		},
		nil,
	).Once()

	// both tool calls must be dispatched
	env.OnActivity(toolActs.DispatchTool, mock.Anything, mock.MatchedBy(func(in types.ToolCallInput) bool {
		return in.CallID == "c1"
	})).Return(types.ToolCallOutput{Result: "result-a"}, nil).Once()

	env.OnActivity(toolActs.DispatchTool, mock.Anything, mock.MatchedBy(func(in types.ToolCallInput) bool {
		return in.CallID == "c2"
	})).Return(types.ToolCallOutput{Result: "result-b"}, nil).Once()

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.Anything).Return(
		types.LLMCallOutput{Content: "done", Done: true},
		nil,
	).Once()

	env.ExecuteWorkflow(AgentLoopWorkflow, baseInput())
	result := mustAgentResult(t, env)

	toolMsgs := 0
	for _, m := range result.Findings {
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Errorf("tool messages = %d, want 2", toolMsgs)
	}
}

func TestAgentLoopToolFailureContinues(t *testing.T) {
	env := newAgentEnv(t)

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.Anything).Return(
		types.LLMCallOutput{
			Done: false,
			ToolCalls: []types.ToolCall{
				{ID: "bad", Name: "web_search", Args: json.RawMessage(`{"query":"x"}`)},
			},
		},
		nil,
	).Once()

	// permanent so the activity is not retried 3 times by ToolCallOptions
	env.OnActivity(toolActs.DispatchTool, mock.Anything, mock.Anything).Return(
		types.ToolCallOutput{},
		temporal.NewNonRetryableApplicationError("search down", "PermanentError", nil),
	).Once()

	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.Anything).Return(
		types.LLMCallOutput{Content: "gave up searching", Done: true},
		nil,
	).Once()

	env.ExecuteWorkflow(AgentLoopWorkflow, baseInput())
	result := mustAgentResult(t, env)

	if !hasContent(result.Findings, "failed") {
		t.Error("expected tool failure text in findings")
	}
}

func TestAgentLoopLLMFails(t *testing.T) {
	// llm failure must fail the workflow not return partial success
	env := newAgentEnv(t)

	// permanent so llm retry policy does not re call the mock
	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.Anything).Return(
		types.LLMCallOutput{},
		temporal.NewNonRetryableApplicationError("llm down", "PermanentError", nil),
	).Once()

	env.ExecuteWorkflow(AgentLoopWorkflow, baseInput())

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error when llm fails")
	}
}

func TestAgentLoopMaxIterations(t *testing.T) {
	env := newAgentEnv(t)

	// must always ask for a tool so the loop hits the cap
	env.OnActivity(llmActs.GenericLLMCall, mock.Anything, mock.Anything).Return(
		types.LLMCallOutput{
			Done: false,
			ToolCalls: []types.ToolCall{
				{ID: "loop", Name: "web_search", Args: json.RawMessage(`{"query":"again"}`)},
			},
		},
		nil,
	)

	env.OnActivity(toolActs.DispatchTool, mock.Anything, mock.Anything).Return(
		types.ToolCallOutput{Result: "still nothing useful"},
		nil,
	)

	input := baseInput()
	input.MaxIterations = 2

	env.ExecuteWorkflow(AgentLoopWorkflow, input)
	result := mustAgentResult(t, env)

	// should still return findings and not hang or error
	if len(result.Findings) == 0 {
		t.Error("expected some findings even after max iterations")
	}
}

func mustAgentResult(t *testing.T, env *testsuite.TestWorkflowEnvironment) types.ResearchResult {
	t.Helper()
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var result types.ResearchResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	return result
}

func hasRole(msgs []types.AgentMessage, role string) bool {
	for _, m := range msgs {
		if m.Role == role {
			return true
		}
	}
	return false
}

func hasContent(msgs []types.AgentMessage, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}
