package config

import (
	"fmt"
	"os"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type Config struct {
	GeminiAPIKey   string
	TemporalHost   string
	TaskQueue      string
	PlanModel      string
	ResearchModel  string
	SynthesisModel string
}

func Load() (*Config, error) {
	cfg := &Config{

		GeminiAPIKey:   os.Getenv("GEMINI_API_KEY"),
		TemporalHost:   os.Getenv("TEMPORAL_HOST_PORT"),
		TaskQueue:      os.Getenv("TASK_QUEUE"),
		PlanModel:      os.Getenv("PLAN_MODEL"),
		ResearchModel:  os.Getenv("RESEARCH_MODEL"),
		SynthesisModel: os.Getenv("SYNTHESIS_MODEL"),
	}

	return cfg, nil
}

// activity option preset for all llm API calls
var LLMCallOptions = workflow.ActivityOptions{
	// High enough to handle slow synthesis generations over a wire
	StartToCloseTimeout:    90 * time.Second,
	ScheduleToCloseTimeout: 10 * time.Minute, // give 10mins to avoid violating gemini free api quota rate limits
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:        4 * time.Second,
		BackoffCoefficient:     2.0,
		MaximumInterval:        60 * time.Second, // avoid gemini cool off period
		MaximumAttempts:        0,
		NonRetryableErrorTypes: []string{"PermanentError"},
	},
}

// activity option preset for tool execs
var ToolCallOptions = workflow.ActivityOptions{
	StartToCloseTimeout:    30 * time.Second,
	ScheduleToCloseTimeout: 2 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts:        3,
		InitialInterval:        1 * time.Second,
		BackoffCoefficient:     2.0,
		NonRetryableErrorTypes: []string{"PermanentError"},
	},
}

// for activity that may take many minutes
var LongRunningOptions = workflow.ActivityOptions{
	StartToCloseTimeout:    5 * time.Minute,
	ScheduleToCloseTimeout: 30 * time.Minute,
	HeartbeatTimeout:       30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts:        2,
		InitialInterval:        5 * time.Second,
		BackoffCoefficient:     2.0,
		NonRetryableErrorTypes: []string{"PermanentError"},
	},
}

func Defaults() *Config {
	cfg, err := Load()
	if err != nil {
		panic(fmt.Sprintf("failed to load config: %v", err))
	}
	return cfg
}
