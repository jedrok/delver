package pipeline

import (
	"strings"
	"testing"

	"github.com/jedrok/delver/internal/types"
)

func TestParsePlanResponseOK(t *testing.T) {
	in := `{"sub_questions":["why is the sky blue","what is rayleigh scattering"]}`

	plan, err := parsePlanResponse(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.SubQuestions) != 2 {
		t.Fatalf("got %d sub-questions, want 2", len(plan.SubQuestions))
	}
	if plan.SubQuestions[0] != "why is the sky blue" {
		t.Errorf("first = %q", plan.SubQuestions[0])
	}
}

func TestParsePlanResponseEmpty(t *testing.T) {
	_, err := parsePlanResponse(`{"sub_questions":[]}`)
	if err == nil {
		t.Fatal("expected error for zero sub-questions")
	}
	if !strings.Contains(err.Error(), "zero sub-questions") {
		t.Errorf("error = %q", err)
	}
}

func TestParsePlanResponseBadJSON(t *testing.T) {
	_, err := parsePlanResponse(`not json at all`)
	if err == nil {
		t.Fatal("expected error for bad json")
	}
	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("error = %q", err)
	}
}

func TestBuildSynthesisPrompt(t *testing.T) {
	results := []types.ResearchResult{
		{
			SubQuestion: "what is water",
			Findings: []types.AgentMessage{
				{Role: "user", Content: "should be skipped"},
				{Role: "assistant", Content: "water is H2O"},
				{Role: "tool", Content: "from wikipedia: H2O"},
			},
		},
	}

	msgs := buildSynthesisPrompt("explain water", results)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q", msgs[0].Role)
	}

	body := msgs[0].Content
	// question and sub question should show up
	if !strings.Contains(body, "explain water") {
		t.Error("missing original question")
	}
	if !strings.Contains(body, "what is water") {
		t.Error("missing sub-question")
	}
	// only assistant + tool findings
	if !strings.Contains(body, "water is H2O") {
		t.Error("missing assistant finding")
	}
	if !strings.Contains(body, "from wikipedia: H2O") {
		t.Error("missing tool finding")
	}
	if strings.Contains(body, "should be skipped") {
		t.Error("user role finding should not be included")
	}
}
