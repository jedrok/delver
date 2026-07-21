package config

import (
	"strings"
	"testing"
)

// set all required env vars so Load can succeed
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("TEMPORAL_HOST_PORT", "localhost:7233")
	t.Setenv("TASK_QUEUE", "delver-tasks")
	t.Setenv("PLAN_MODEL", "gemini-2.5-flash")
	t.Setenv("RESEARCH_MODEL", "gemini-2.5-flash")
	t.Setenv("SYNTHESIS_MODEL", "gemini-2.5-flash")
}

func TestLoadOK(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("REQUIRE_APPROVAL", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GeminiAPIKey != "test-key" {
		t.Errorf("GeminiAPIKey = %q", cfg.GeminiAPIKey)
	}
	if cfg.TemporalHost != "localhost:7233" {
		t.Errorf("TemporalHost = %q", cfg.TemporalHost)
	}
	if cfg.TaskQueue != "delver-tasks" {
		t.Errorf("TaskQueue = %q", cfg.TaskQueue)
	}
	if !cfg.RequireApproval {
		t.Error("RequireApproval should be true")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	// each case clears for one required key
	keys := []string{
		"GEMINI_API_KEY",
		"TEMPORAL_HOST_PORT",
		"TASK_QUEUE",
		"PLAN_MODEL",
		"RESEARCH_MODEL",
		"SYNTHESIS_MODEL",
	}

	for _, missing := range keys {
		t.Run(missing, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(missing, "")

			_, err := Load()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error %q should mention %s", err, missing)
			}
		})
	}
}

func TestEnvTruthy(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "yes", "YES", "on", "ON"}
	for _, v := range truthy {
		if !envTruthy(v) {
			t.Errorf("envTruthy(%q) = false, want true", v)
		}
	}

	falsy := []string{"", "false", "FALSE", "0", "no", "off", "maybe"}
	for _, v := range falsy {
		if envTruthy(v) {
			t.Errorf("envTruthy(%q) = true, want false", v)
		}
	}
}
