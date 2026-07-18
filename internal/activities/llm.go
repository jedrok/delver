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

	errStr := err.Error()

	// daily free tier / hard account caps will not clear inside retry window
	// check before the generic 429 path so we do not spin until ScheduleToClose
	if isNonRetryableQuota(errStr) {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("gemini quota exhausted: %v", err),
			"PermanentError",
			err,
		)
	}

	// short throttles/temporal chills out
	if isRetryableRateLimit(errStr) {
		return temporal.NewApplicationError(
			fmt.Sprintf("upstream rate limit hit (will retry): %v", err),
			"RateLimitError",
		)
	}

	var apiErr *openai.APIError
	if !errors.As(err, &apiErr) {
		return temporal.NewApplicationError(
			fmt.Sprintf("gemini request failed: %v", err),
			"TransientError",
		)
	}

	switch apiErr.HTTPStatusCode {

	case 401, 403:
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("gemini auth failed: %v", err),
			"PermanentError",
			err,
		)

	case 400, 422:
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("gemini bad request: %v", err),
			"PermanentError",
			err,
		)

	case 429:
		msg := apiErr.Message
		if isNonRetryableQuota(msg) || isNonRetryableQuota(errStr) {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("gemini quota exhausted: %v", err),
				"PermanentError",
				err,
			)
		}
		return temporal.NewApplicationError(
			fmt.Sprintf("gemini rate limited: %v", err),
			"RateLimitError",
		)

	default:
		return temporal.NewApplicationError(
			fmt.Sprintf("gemini server error: %v", err),
			"TransientError",
		)
	}
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
