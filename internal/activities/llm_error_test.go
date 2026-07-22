package activities

import (
	"errors"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
	"go.temporal.io/sdk/temporal"
)

// check temporal error type and whether it is permanent
func assertAppErr(t *testing.T, err error, expectedType string, nonRetryable bool) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("want ApplicationError, got %T: %v", err, err)
	}
	if appErr.Type() != expectedType {
		t.Errorf("type = %q, want %q", appErr.Type(), expectedType)
	}
	if appErr.NonRetryable() != nonRetryable {
		t.Errorf("NonRetryable = %v, want %v", appErr.NonRetryable(), nonRetryable)
	}
}

func assertNoJSONNoise(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("error still has json parse noise: %q", err)
	}
	if strings.Contains(err.Error(), `"error"`) {
		t.Errorf("error still dumps raw json body: %q", err)
	}
}

func TestClassifyOpenAIErrorNil(t *testing.T) {
	if got := classifyOpenAIError(nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestClassifyOpenAIErrorAuth(t *testing.T) {
	for _, code := range []int{401, 403} {
		err := &openai.APIError{HTTPStatusCode: code, Message: "bad key"}
		got := classifyOpenAIError(err)
		assertAppErr(t, got, "PermanentError", true)
	}
}

func TestClassifyOpenAIErrorBadRequest(t *testing.T) {
	for _, code := range []int{400, 422} {
		err := &openai.APIError{HTTPStatusCode: code, Message: "bad body"}
		got := classifyOpenAIError(err)
		assertAppErr(t, got, "PermanentError", true)
	}
}

func TestClassifyOpenAIErrorDailyQuota(t *testing.T) {
	// daily caps should not be retried forever
	err := errors.New("quota: GenerateRequestsPerDay exceeded")
	got := classifyOpenAIError(err)
	assertAppErr(t, got, "PermanentError", true)
}

func TestClassifyOpenAIErrorHardQuota(t *testing.T) {
	err := errors.New("you exceeded your current quota for this project")
	got := classifyOpenAIError(err)
	assertAppErr(t, got, "PermanentError", true)
}

func TestClassifyOpenAIErrorFreeTier429(t *testing.T) {
	// free tier / per minute throttles are retryable
	err := errors.New("429 free_tier PerMinute resource exhausted")
	got := classifyOpenAIError(err)
	assertAppErr(t, got, "RateLimitError", false)
}

func TestClassifyOpenAIErrorAPIRateLimit(t *testing.T) {
	err := &openai.APIError{HTTPStatusCode: 429, Message: "slow down"}
	got := classifyOpenAIError(err)
	assertAppErr(t, got, "RateLimitError", false)
}

func TestClassifyOpenAIErrorServer(t *testing.T) {
	err := &openai.APIError{HTTPStatusCode: 500, Message: "boom"}
	got := classifyOpenAIError(err)
	assertAppErr(t, got, "TransientError", false)
}

func TestClassifyOpenAIErrorGeneric(t *testing.T) {
	// plain errors with no api status become transient
	got := classifyOpenAIError(errors.New("connection reset"))
	assertAppErr(t, got, "TransientError", false)
}

func TestClassifyOpenAIErrorGeminiArrayBody(t *testing.T) {
	// gemini free-tier 429 body as a json array. go-openai fails to parse it
	body := []byte(`[{
  "error": {
    "code": 429,
    "message": "You exceeded your current quota, please check your plan and billing details.\n* Quota exceeded for metric: generate_content_free_tier_requests\n* quotaId: GenerateRequestsPerMinutePerProjectPerModel-FreeTier",
    "status": "RESOURCE_EXHAUSTED"
  }
}]`)
	raw := &openai.RequestError{
		HTTPStatusCode: 429,
		HTTPStatus:     "429 Too Many Requests",
		Err:            errors.New("json: cannot unmarshal array into Go value of type openai.ErrorResponse"),
		Body:           body,
	}

	got := classifyOpenAIError(raw)
	assertAppErr(t, got, "RateLimitError", false)
	assertNoJSONNoise(t, got)

	if !strings.Contains(got.Error(), "RESOURCE_EXHAUSTED") &&
		!strings.Contains(got.Error(), "exceeded your current quota") {
		t.Errorf("expected gemini message in error, got %q", got)
	}
}

func TestGeminiMessageFromBodyArray(t *testing.T) {
	body := []byte(`[{"error":{"message":"slow down\nmore detail","status":"RESOURCE_EXHAUSTED"}}]`)
	msg := geminiMessageFromBody(body)
	if msg != "RESOURCE_EXHAUSTED: slow down" {
		t.Errorf("got %q", msg)
	}
}

func TestGeminiMessageFromBodyObject(t *testing.T) {
	body := []byte(`{"error":{"message":"bad key","status":"PERMISSION_DENIED"}}`)
	msg := geminiMessageFromBody(body)
	if msg != "PERMISSION_DENIED: bad key" {
		t.Errorf("got %q", msg)
	}
}
