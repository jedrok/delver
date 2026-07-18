# delver

Research helper built on [Temporal](https://temporal.io/) and Gemini.

You ask a question. Delver breaks it into smaller questions, researches each one, writes one report.

## What it does

1. **Plan** — splits your question into sub-questions  
2. **Research** — runs an agent loop per sub-question  
3. **Synthesize** — merges findings into one report  
4. **Approve** — optional human approve/reject gate  

## Prerequisites

- Go 1.22+  
- [Temporal CLI](https://docs.temporal.io/cli) (`temporal`)  
- A [Gemini API key](https://ai.google.dev/)  

## Setup

1. Clone the repo and enter it:

```bash
cd delver
```

2. Create a `.env` in the repo root (this file is gitignored):

```bash
GEMINI_API_KEY=your_key_here
TEMPORAL_HOST_PORT=localhost:7233
TASK_QUEUE=delver-tasks
PLAN_MODEL=gemini-2.5-flash
RESEARCH_MODEL=gemini-2.5-flash
SYNTHESIS_MODEL=gemini-2.5-flash
```

Optional:

```bash
REQUIRE_APPROVAL=true   # default is off
```

Install Go deps:

```bash
go mod tidy
```

## Run (3 terminals, in order)

```bash
# 1
temporal server start-dev

# 2
go run ./cmd/worker

# 3
go run ./cmd/client start "why is the sky blue"
```

Default: waits and prints the report. UI: http://localhost:8233



## Commands

Always use the **parent** workflow id (`delver-…`)

| Command                               | What it does                                       |
| ------------------------------------- | -------------------------------------------------- |
| `start "question"`                    | Start research; waits and prints report by default |
| `start --require-approval "question"` | Same, but pauses for human approve/reject          |
| `start --detach "question"`           | Start and return immediately                       |
| `status <id>`                         | Is it researching, waiting for approval, done?     |
| `approve <id>`                        | Approve a waiting run and print the report         |
| `reject <id>`                         | Reject a waiting run                               |
| `result <id>`                         | Fetch the final report (for detached runs)         |
| `cancel <id>`                         | Stop the run (children stop too)                   |
| `cancel --force <id>`                 | Hard kill if cancel isn’t enough                   |

`Ctrl-C` does not stop research — use `cancel`.
