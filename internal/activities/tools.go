package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/jedrok/delver/internal/types"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

type ToolHandler func(ctx context.Context, args json.RawMessage) (string, error)

type ToolActivities struct {
	registry map[string]ToolHandler
}

// create ToolActivities and register all available tools
func NewToolActivities() *ToolActivities {
	ta := &ToolActivities{
		registry: make(map[string]ToolHandler),
	}
	ta.registry["web_search"] = ta.webSearch
	ta.registry["fetch_page"] = ta.fetchPage

	return ta
}

func ResearchTools() []types.ToolDef {
	return []types.ToolDef{
		{
			Name:        "web_search",
			Description: "Search the web. Returns a short summary when one is available.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"search query"}},"required":["query"]}`),
		},
		{
			Name:        "fetch_page",
			Description: "Fetch a URL and return a text excerpt of the page.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"full URL to fetch"}},"required":["url"]}`),
		},
	}
}

// route tool call to the correct handler by name
func (a *ToolActivities) DispatchTool(ctx context.Context,
	input types.ToolCallInput) (types.ToolCallOutput, error) {

	logger := activity.GetLogger(ctx)
	logger.Info("dispatching tool", "tool", input.ToolName)

	handler, ok := a.registry[input.ToolName]
	if !ok {
		// unknown tool
		return types.ToolCallOutput{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("unknown tool: %s", input.ToolName),
			"PermanentError",
			nil,
		)
	}

	result, err := handler(ctx, input.ToolArgs)
	if err != nil {
		return types.ToolCallOutput{}, err
	}

	return types.ToolCallOutput{Result: result}, nil
}

type webSearchArgs struct {
	Query string `json:"query"`
}

func (a *ToolActivities) webSearch(ctx context.Context,
	args json.RawMessage) (string, error) {

	var input webSearchArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid web_search args: %v", err),
			"PermanentError",
			err,
		)
	}
	if input.Query == "" {
		return "", temporal.NewNonRetryableApplicationError(
			"web_search requires a query",
			"PermanentError",
			nil,
		)
	}

	// must encode query spaces and "?" break the url and nginx returns 400 html
	endpoint := duckDuckGoURL(input.Query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("web_search bad request: %v", err),
			"PermanentError",
			err,
		)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", temporal.NewApplicationError(
			fmt.Sprintf("web_search request failed: %v", err),
			"TransientError",
		)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", temporal.NewApplicationError(
			fmt.Sprintf("web_search read failed: %v", err),
			"TransientError",
		)
	}

	// avoid feeding html error pages back to the model as search results
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", temporal.NewApplicationError(
			fmt.Sprintf("web_search http %d", resp.StatusCode),
			"TransientError",
		)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", temporal.NewApplicationError(
			fmt.Sprintf("web_search bad json: %v", err),
			"TransientError",
		)
	}

	abstract, ok := result["Abstract"].(string)
	if !ok || abstract == "" {
		// fallback
		if fallback, ok := result["abstract"].(string); ok && fallback != "" {
			return fallback, nil
		}
		return fmt.Sprintf("No instant answer found for: %s", input.Query), nil
	}

	return abstract, nil
}

func duckDuckGoURL(query string) string {
	return "https://api.duckduckgo.com/?" + url.Values{
		"q":       {query},
		"format":  {"json"},
		"no_html": {"1"},
	}.Encode()
}

type fetchPageArgs struct {
	URL string `json:"url"`
}

func (a *ToolActivities) fetchPage(ctx context.Context,
	args json.RawMessage) (string, error) {

	var input fetchPageArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid fetch_page args: %v", err),
			"PermanentError",
			err,
		)
	}
	if input.URL == "" {
		return "", temporal.NewNonRetryableApplicationError(
			"fetch_page requires a url",
			"PermanentError",
			nil,
		)
	}

	resp, err := http.Get(input.URL)
	if err != nil {
		return "", temporal.NewApplicationError(
			fmt.Sprintf("fetch_page request failed: %v", err),
			"TransientError",
		)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", temporal.NewApplicationError(
			fmt.Sprintf("fetch_page read failed: %v", err),
			"TransientError",
		)
	}

	// truncate to 2000 chars to save tokens
	content := string(body)
	if len(content) > 2000 {
		content = content[:2000] + "... [truncated]"
	}
	return content, nil
}
