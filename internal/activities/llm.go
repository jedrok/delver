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

	// check the model response to figure out what it actually wants to do next
	choice := resp.Choices[0]
	var toolCall *types.ToolCall

	if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
		call := choice.Message.ToolCalls[0]
		toolCall = &types.ToolCall{
			Name: call.Function.Name,
			Args: json.RawMessage(call.Function.Arguments),
		}
	}

	done := choice.FinishReason == "stop"

	return types.LLMCallOutput{
		Content:  choice.Message.Content,
		Done:     done,
		ToolCall: toolCall,
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
			},
		}
	}
	return result
}

func toOpenAIMessages(messages []types.AgentMessage) []openai.ChatCompletionMessage {
	result := make([]openai.ChatCompletionMessage, len(messages))
	for i, m := range messages {
		result[i] = openai.ChatCompletionMessage{
			Role:    m.Role,
			Content: m.Content,
		}
	}
	return result
}

func classifyOpenAIError(err error) error {
	var apiErr *openai.APIError

	if !errors.As(err, &apiErr) {
		return temporal.NewApplicationError(
			fmt.Sprintf("openai request failed: %v", err),
			"TransientError",
		)
	}

	switch apiErr.HTTPStatusCode {

	case 401, 403:
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("openai auth failed: %v", err),
			"PermanentError",
			err,
		)

	case 400, 422:
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("openai bad request: %v", err),
			"PermanentError",
			err,
		)

	case 429:
		// quota is over not recoverable by retryin
		if strings.Contains(apiErr.Message, "exceeded your current quota") {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("openai quota exceeded: %v", err),
				"PermanentError",
				err,
			)
		}
		return temporal.NewApplicationError(
			fmt.Sprintf("openai rate limited: %v", err),
			"RateLimitError",
		)

	default:
		return temporal.NewApplicationError(
			fmt.Sprintf("openai server error: %v", err),
			"TransientError",
		)
	}
}
