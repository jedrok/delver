package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jedrok/delver/internal/config"
	"github.com/jedrok/delver/internal/types"
	openai "github.com/sashabaranov/go-openai"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

type LLMActivities struct {
	client *openai.Client
	config *config.Config
}

func NewLLMActivities(cfg *config.Config) *LLMActivities {
	clientConfig := openai.DefaultConfig(cfg.GeminiAPIKey)

	clientConfig.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"

	client := openai.NewClientWithConfig(clientConfig)

	return &LLMActivities{
		client: client,
		config: cfg,
	}
}

func (a *LLMActivities) GenericLLMCall(ctx context.Context, input types.LLMCallInput) (types.LLMCallOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("calling LLM", "model", input.Model, "messages", len(input.Messages))

	req := openai.ChatCompletionRequest{
		Model:    input.Model,
		Messages: toOpenAIMessages(input.Messages),
		Tools:    toOpenAITools(input.Tools),
	}

	resp, err := a.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return types.LLMCallOutput{}, classifyOpenAIError(err)
	}

	if len(resp.Choices) == 0 {
		return types.LLMCallOutput{}, temporal.NewApplicationError(
			"LLM returned no choices",
			"TransientError",
		)
	}

	choice := resp.Choices[0]
	toolCalls := make([]types.ToolCall, 0, len(choice.Message.ToolCalls))
	for _, call := range choice.Message.ToolCalls {
		toolCalls = append(toolCalls, types.ToolCall{
			ID:   call.ID,
			Name: call.Function.Name,
			Args: json.RawMessage(call.Function.Arguments),
		})
	}

	return types.LLMCallOutput{
		Content:   choice.Message.Content,
		Done:      len(toolCalls) == 0,
		ToolCalls: toolCalls,
	}, nil
}

func toOpenAITools(tools []types.ToolDef) []openai.Tool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]openai.Tool, len(tools))
	for i, t := range tools {
		result[i] = openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		}
	}
	return result
}

func toOpenAIMessages(messages []types.AgentMessage) []openai.ChatCompletionMessage {
	result := make([]openai.ChatCompletionMessage, len(messages))
	for i, m := range messages {
		msg := openai.ChatCompletionMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: string(tc.Args),
				},
			})
		}
		result[i] = msg
	}
	return result
}

func classifyOpenAIError(err error) error {
	if err == nil {
		return nil
	}

	// match on the full raw string. including response body. logs get a short summary
	errStr := err.Error()
	detail := summarizeGeminiError(err)

	// daily free tier / hard account caps will not clear inside retry window
	// check before the generic 429 path so we do not spin until ScheduleToClose
	if isNonRetryableQuota(errStr) {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("gemini quota exhausted: %s", detail),
			"PermanentError",
			err,
		)
	}

	// short throttles/temporal chills out
	if isRetryableRateLimit(errStr) {
		return temporal.NewApplicationError(
			fmt.Sprintf("upstream rate limit hit (will retry): %s", detail),
			"RateLimitError",
		)
	}

	if apiErr, ok := errors.AsType[*openai.APIError](err); ok {
		return classifyByStatus(apiErr.HTTPStatusCode, detail, err)
	}
	// RequestError still has a status when the openai body parser failed
	if reqErr, ok := errors.AsType[*openai.RequestError](err); ok && reqErr.HTTPStatusCode > 0 {
		return classifyByStatus(reqErr.HTTPStatusCode, detail, err)
	}
	return temporal.NewApplicationError(
		fmt.Sprintf("gemini request failed: %s", detail),
		"TransientError",
	)
}

func classifyByStatus(code int, detail string, cause error) error {
	switch code {
	case 401, 403:
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("gemini auth failed: %s", detail),
			"PermanentError",
			cause,
		)
	case 400, 422:
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("gemini bad request: %s", detail),
			"PermanentError",
			cause,
		)
	case 429:
		// use full cause text here so free_tier / PerMinute markers are not lost
		if isNonRetryableQuota(cause.Error()) {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("gemini quota exhausted: %s", detail),
				"PermanentError",
				cause,
			)
		}
		return temporal.NewApplicationError(
			fmt.Sprintf("gemini rate limited: %s", detail),
			"RateLimitError",
		)
	default:
		return temporal.NewApplicationError(
			fmt.Sprintf("gemini server error: %s", detail),
			"TransientError",
		)
	}
}

// pull short human message out of go-openai errors
// gemini sometimes returns a json array body and go-openai then puts the raw body
// and cannot unmarshal array noise into err.Error() so we strip that here
func summarizeGeminiError(err error) string {
	if err == nil {
		return ""
	}

	if reqErr, ok := errors.AsType[*openai.RequestError](err); ok {
		if msg := geminiMessageFromBody(reqErr.Body); msg != "" {
			if reqErr.HTTPStatusCode > 0 {
				return fmt.Sprintf("status %d: %s", reqErr.HTTPStatusCode, msg)
			}
			return msg
		}
		if reqErr.HTTPStatusCode > 0 {
			return fmt.Sprintf("status %d: %s", reqErr.HTTPStatusCode, reqErr.HTTPStatus)
		}
	}

	if apiErr, ok := errors.AsType[*openai.APIError](err); ok {
		if apiErr.Message != "" {
			return firstLine(apiErr.Message)
		}
		return firstLine(apiErr.Error())
	}

	return firstLine(err.Error())
}

// gemini openai-compat errors are usually {"error":{...}} but free-tier 429s
// sometimes arrive as a one-element array of that object.
func geminiMessageFromBody(body []byte) string {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		return ""
	}

	payload := body
	if body[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil || len(arr) == 0 {
			return ""
		}
		payload = arr[0]
	}

	var wrap struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &wrap); err != nil || wrap.Error.Message == "" {
		return ""
	}

	msg := firstLine(wrap.Error.Message)
	if wrap.Error.Status != "" {
		return wrap.Error.Status + ": " + msg
	}
	return msg
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	before, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(before)
}

func isNonRetryableQuota(s string) bool {
	if strings.Contains(s, "PerDay") ||
		strings.Contains(s, "per day") ||
		strings.Contains(s, "GenerateRequestsPerDay") {
		return true
	}
	// paid account hard cap which is not free tier per minute throttle
	if strings.Contains(s, "exceeded your current quota") &&
		!strings.Contains(s, "free_tier") &&
		!strings.Contains(s, "PerMinute") {
		return true
	}
	return false
}

func isRetryableRateLimit(s string) bool {
	return strings.Contains(s, "429") ||
		strings.Contains(s, "RESOURCE_EXHAUSTED") ||
		strings.Contains(s, "free_tier") ||
		strings.Contains(s, "PerMinute") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "Rate limit")
}
