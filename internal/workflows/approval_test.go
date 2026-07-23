package workflows

import (
	"testing"
	"time"

	"github.com/jedrok/delver/internal/types"
	"go.temporal.io/sdk/testsuite"
)

// each test starts a clean env
func newApprovalEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ApprovalGateWorkflow)
	return env
}

func runApproval(
	t *testing.T,
	env *testsuite.TestWorkflowEnvironment,
	input types.ApprovalGateInput,
) types.ApprovalResult {
	t.Helper()

	env.ExecuteWorkflow(ApprovalGateWorkflow, input)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	var result types.ApprovalResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	return result
}

func TestApprovalGateApprove(t *testing.T) {
	env := newApprovalEnv(t)
	report := "draft research report"

	// fire signal almost immediately. timer is long so signal wins
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ApprovalSignaleName, types.ApprovalDecision{
			Action: "approve",
		})
	}, time.Millisecond)

	result := runApproval(t, env, types.ApprovalGateInput{
		Report:  report,
		Timeout: time.Hour,
	})

	if result.Status != "approved" {
		t.Errorf("status = %q, want approved", result.Status)
	}
	if result.Report != report {
		t.Errorf("report = %q, want %q", result.Report, report)
	}
}

func TestApprovalGateReject(t *testing.T) {
	env := newApprovalEnv(t)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ApprovalSignaleName, types.ApprovalDecision{
			Action: "reject",
		})
	}, time.Millisecond)

	result := runApproval(t, env, types.ApprovalGateInput{
		Report:  "should not be returned",
		Timeout: time.Hour,
	})

	if result.Status != "rejected" {
		t.Errorf("status = %q, want rejected", result.Status)
	}
	if result.Report != "" {
		t.Errorf("report should be empty on reject, got %q", result.Report)
	}
}

func TestApprovalGateGarbageIsReject(t *testing.T) {
	// only exact "approve" counts. anything else is reject
	env := newApprovalEnv(t)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ApprovalSignaleName, types.ApprovalDecision{
			Action: "yes",
		})
	}, time.Millisecond)

	result := runApproval(t, env, types.ApprovalGateInput{
		Report:  "nope",
		Timeout: time.Hour,
	})

	if result.Status != "rejected" {
		t.Errorf("status = %q, want rejected", result.Status)
	}
}

func TestApprovalGateTimeout(t *testing.T) {
	// no signal so testsuite skips the timer so this finishes right away
	env := newApprovalEnv(t)

	result := runApproval(t, env, types.ApprovalGateInput{
		Report:  "late",
		Timeout: time.Hour,
	})

	if result.Status != "timed_out" {
		t.Errorf("status = %q, want timed_out", result.Status)
	}
	if result.Report != "" {
		t.Errorf("report should be empty on timeout, got %q", result.Report)
	}
}

func TestApprovalGateQueryWhileWaiting(t *testing.T) {
	env := newApprovalEnv(t)

	// query while the gate is open then approve so the workflow can finish
	env.RegisterDelayedCallback(func() {
		encoded, err := env.QueryWorkflow("getStatus")
		if err != nil {
			t.Errorf("query failed: %v", err)
			return
		}
		var st types.PipelineStatus
		if err := encoded.Get(&st); err != nil {
			t.Errorf("decode status: %v", err)
			return
		}
		if st.Phase != "approval" {
			t.Errorf("phase = %q, want approval", st.Phase)
		}
		if st.Status != "waiting" {
			t.Errorf("status = %q, want waiting", st.Status)
		}

		env.SignalWorkflow(ApprovalSignaleName, types.ApprovalDecision{
			Action: "approve",
		})
	}, time.Millisecond)

	result := runApproval(t, env, types.ApprovalGateInput{
		Report:  "ok",
		Timeout: time.Hour,
	})

	if result.Status != "approved" {
		t.Errorf("status = %q, want approved", result.Status)
	}
}
