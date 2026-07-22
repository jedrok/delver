package activities

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedrok/delver/internal/types"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestResearchTools(t *testing.T) {
	tools := ResearchTools()
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}

	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("%s missing description", tool.Name)
		}
		if len(tool.Parameters) == 0 {
			t.Errorf("%s missing parameters schema", tool.Name)
		}
	}
	if !names["web_search"] || !names["fetch_page"] {
		t.Errorf("tools = %v, want web_search and fetch_page", names)
	}
}

func TestWebSearchBadArgs(t *testing.T) {
	ta := NewToolActivities()

	// invalid json
	_, err := ta.webSearch(context.Background(), json.RawMessage(`not-json`))
	if err == nil {
		t.Fatal("expected error for bad json")
	}
	assertPermanent(t, err)

	// empty query
	_, err = ta.webSearch(context.Background(), json.RawMessage(`{"query":""}`))
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	assertPermanent(t, err)
}

func TestDuckDuckGoURLEncodesQuery(t *testing.T) {
	// spaces and "?" must not appear raw in the query string
	got := duckDuckGoURL("What is the capital of Uganda?")
	if strings.Contains(got, " ") {
		t.Errorf("url still has spaces: %q", got)
	}
	if strings.Contains(got, "Uganda?") {
		t.Errorf("raw ? in query value breaks the url: %q", got)
	}
	if !strings.Contains(got, "What") || !strings.Contains(got, "Uganda") {
		t.Errorf("missing query text: %q", got)
	}
	if !strings.Contains(got, "format=json") {
		t.Errorf("missing format=json: %q", got)
	}
}

func TestFetchPageBadArgs(t *testing.T) {
	ta := NewToolActivities()

	_, err := ta.fetchPage(context.Background(), json.RawMessage(`{`))
	if err == nil {
		t.Fatal("expected error for bad json")
	}
	assertPermanent(t, err)

	_, err = ta.fetchPage(context.Background(), json.RawMessage(`{"url":""}`))
	if err == nil {
		t.Fatal("expected error for empty url")
	}
	assertPermanent(t, err)
}

func TestFetchPageTruncates(t *testing.T) {
	// long body so we hit the 2000 char cap
	long := strings.Repeat("a", 3000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()

	ta := NewToolActivities()
	args, _ := json.Marshal(map[string]string{"url": srv.URL})

	got, err := ta.fetchPage(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(got, "... [truncated]") {
		t.Errorf("expected truncation marker, got len=%d", len(got))
	}
	// 2000 chars of content + marker
	if len(got) != 2000+len("... [truncated]") {
		t.Errorf("len = %d, want %d", len(got), 2000+len("... [truncated]"))
	}
}

func TestDispatchToolUnknown(t *testing.T) {
	// activity logger needs a real activity env
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	ta := NewToolActivities()
	env.RegisterActivity(ta)

	_, err := env.ExecuteActivity(ta.DispatchTool, types.ToolCallInput{
		ToolName: "not_a_real_tool",
		ToolArgs: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	// testsuite wraps activity error. message should be unknown tool
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %q", err)
	}
}

func assertPermanent(t *testing.T, err error) {
	t.Helper()
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("want ApplicationError, got %T: %v", err, err)
	}
	if appErr.Type() != "PermanentError" {
		t.Errorf("type = %q, want PermanentError", appErr.Type())
	}
	if !appErr.NonRetryable() {
		t.Error("expected non retryable error")
	}
}
