package types

import "encoding/json"

type AgentMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type LLMCallInput struct {
	Model    string         `json:"model"`
	Messages []AgentMessage `json:"messages"`
	Tools    []ToolDef      `json:"tools,omitempty"`
}

type LLMCallOutput struct {
	Content  string    `json:"content"`
	Done     bool      `json:"done"`
	ToolCall *ToolCall `json:"tool_call,omitempty"`
}

type ToolCallInput struct {
	ToolName   string          `json:"tool_name"`
	ToolArgs   json.RawMessage `json:"tool_args"`
	CallID     string          `json:"call_id"`
	PipelineID string          `json:"pipeline_id"`
}
type ToolCallOutput struct {
	Result string `json:"result"`
}

type AgentLoopInput struct {
	Task          string    `json:"task"`
	MaxIterations int       `json:"max_iterations"`
	Model         string    `json:"model"`
	ToolDef       []ToolDef `json:"tool_defs"`
}

type ResearchResult struct {
	SubQuestion string         `json:"sub_questions"`
	Findings    []AgentMessage `json:"findings"`
}

type SynthesisOutput struct {
	Report string `json:"report"`
}

type PipelineInput struct {
	Question  string  `json:"question"`
	BudgetUSD float64 `json:"budget_usd"`
}

type PipelineOutput struct {
	Report    string  `json:"report"`
	Status    string  `json:"status"`
	TotalCost float64 `json:"total_cost"`
}

type ApprovalDecision struct {
	Action string `json:"action"`
}

type ApprovalGateInput struct {
	Report    string `json:"report"`
	TimeoutMs int64  `json:"time_out"`
}

type ApprovalResult struct {
	Report string `json:"report"`
	Status string `json:"status"`
}

type PipelineStatus struct {
	Phase  string `json:"phase"`
	Status string `json:"status"`
}

type PlanOutput struct {
	SubQuestions []string `json:"sub_questions"`
}
