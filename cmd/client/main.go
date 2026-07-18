package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jedrok/delver/internal/config"
	"github.com/jedrok/delver/internal/pipeline"
	"github.com/jedrok/delver/internal/types"
	"github.com/jedrok/delver/internal/workflows"
	"github.com/joho/godotenv"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	if err := godotenv.Load(); err != nil {
		log.Printf("warning: could not load .env: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	c, err := client.Dial(client.Options{HostPort: cfg.TemporalHost})
	if err != nil {
		log.Fatalf("failed to connect to temporal: %v", err)
	}
	defer c.Close()

	switch os.Args[1] {
	case "start":
		os.Exit(runStart(c, cfg, os.Args[2:]))
	case "approve":
		os.Exit(runDecide(c, os.Args[2:], "approve"))
	case "reject":
		os.Exit(runDecide(c, os.Args[2:], "reject"))
	case "status":
		os.Exit(runStatus(c, os.Args[2:]))
	case "result":
		os.Exit(runResult(c, os.Args[2:]))
	case "cancel":
		os.Exit(runCancel(c, os.Args[2:]))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage:
  delver start [--detach] [--require-approval] "question"
  delver approve <workflow-id>
  delver reject  <workflow-id>
  delver status  <workflow-id>
  delver result  <workflow-id>
  delver cancel  [--force] [--reason "..."] <workflow-id>

defaults:
  without approval  start waits and prints the report when done
  with approval     start returns immediately; approve prints the report
  --detach          always return immediately (use result later)
  --wait            always wait for the final result (even with approval;
                    approve from another terminal)
  cancel            stops the parent run (research children stop too)
  cancel --force    hard terminate if cancel is not enough

approval is off by default. enable with --require-approval or REQUIRE_APPROVAL=true.
`)
}

func runStart(c client.Client, cfg *config.Config, args []string) int {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	detach := fs.Bool("detach", false, "return immediately with the workflow id")
	wait := fs.Bool("wait", false, "always wait for the final result")
	requireApproval := fs.Bool("require-approval", cfg.RequireApproval, "wait for human approve/reject after the report")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "start requires a question")
		return 2
	}
	question := fs.Arg(0)

	shouldWait := !*requireApproval
	if *detach {
		shouldWait = false
	}
	if *wait {
		shouldWait = true
	}

	workflowID := fmt.Sprintf("delver-%d", time.Now().UnixNano())
	opts := client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: cfg.TaskQueue,
	}

	we, err := c.ExecuteWorkflow(context.Background(), opts,
		pipeline.ResearchPipelineWorkflow,
		types.PipelineInput{
			Question:        question,
			RequireApproval: *requireApproval,
			ApprovalTimeout: 24 * time.Hour,
		},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start workflow: %v\n", err)
		return 1
	}

	id := we.GetID()
	fmt.Printf("started: %s\n", id)

	if !shouldWait {
		if *requireApproval {
			fmt.Printf("researching… when ready:\n")
			fmt.Printf("  go run ./cmd/client status %s\n", id)
			fmt.Printf("  go run ./cmd/client approve %s\n", id)
			fmt.Printf("  go run ./cmd/client reject  %s\n", id)
		} else {
			fmt.Printf("detached. fetch later with:\n")
			fmt.Printf("  go run ./cmd/client result %s\n", id)
		}
		return 0
	}

	if *requireApproval {
		fmt.Fprintln(os.Stderr, "waiting for research + your approve/reject (approve from another terminal)…")
	} else {
		fmt.Fprintln(os.Stderr, "researching…")
	}

	var result types.PipelineOutput
	if err := we.Get(context.Background(), &result); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline failed: %v\n", err)
		return 1
	}
	printResult(result)
	return 0
}

func runDecide(c client.Client, args []string, action string) int {
	fs := flag.NewFlagSet(action, flag.ExitOnError)
	detach := fs.Bool("detach", false, "send decision and exit without fetching the result")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "%s requires a workflow id\n", action)
		return 2
	}
	parentID := fs.Arg(0)
	approvalID := parentID + "-approval"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := c.SignalWorkflow(ctx, approvalID, "", workflows.ApprovalSignaleName, types.ApprovalDecision{
		Action: action,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to %s: %v\n", action, err)
		fmt.Fprintf(os.Stderr, "tip: run status first — the gate only exists after research finishes\n")
		return 1
	}

	fmt.Printf("%s sent\n", action)
	if *detach {
		fmt.Printf("next: go run ./cmd/client result %s\n", parentID)
		return 0
	}

	fmt.Fprintln(os.Stderr, "fetching result…")
	return fetchAndPrint(c, parentID)
}

func runStatus(c client.Client, args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "status requires a workflow id")
		return 2
	}
	parentID := fs.Arg(0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	desc, err := c.DescribeWorkflowExecution(ctx, parentID, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to describe workflow: %v\n", err)
		return 1
	}
	info := desc.GetWorkflowExecutionInfo()
	status := info.GetStatus()
	fmt.Printf("workflow: %s\n", parentID)
	fmt.Printf("status:   %s\n", status)

	qCtx, qCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer qCancel()
	val, err := c.QueryWorkflow(qCtx, parentID+"-approval", "", "getStatus")
	if err != nil {
		switch status {
		case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
			fmt.Println("phase:    researching (approval gate not open yet)")
			fmt.Println("tip:      keep the worker running; check again in a minute")
		case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
			fmt.Println("phase:    done (no approval gate on this run)")
			fmt.Printf("next:     go run ./cmd/client result %s\n", parentID)
		default:
			fmt.Printf("phase:    n/a (%s)\n", strings.ToLower(status.String()))
		}
		return 0
	}

	var st types.PipelineStatus
	if err := val.Get(&st); err != nil {
		fmt.Fprintf(os.Stderr, "failed to decode approval status: %v\n", err)
		return 1
	}
	fmt.Printf("approval: phase=%s status=%s\n", st.Phase, st.Status)
	switch st.Status {
	case "waiting":
		fmt.Printf("next:     go run ./cmd/client approve %s\n", parentID)
		fmt.Printf("          go run ./cmd/client reject  %s\n", parentID)
	case "approved", "rejected", "timed_out":
		if status == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED {
			fmt.Printf("next:     go run ./cmd/client result %s\n", parentID)
		}
	}
	return 0
}

func runResult(c client.Client, args []string) int {
	fs := flag.NewFlagSet("result", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "result requires a workflow id")
		return 2
	}
	return fetchAndPrint(c, fs.Arg(0))
}

func runCancel(c client.Client, args []string) int {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	force := fs.Bool("force", false, "terminate immediately instead of requesting cancel")
	reason := fs.String("reason", "stopped by user", "reason recorded on terminate (--force)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "cancel requires a workflow id (the parent delver-… id)")
		return 2
	}
	workflowID := fs.Arg(0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var err error
	if *force {
		err = c.TerminateWorkflow(ctx, workflowID, "", *reason)
	} else {
		err = c.CancelWorkflow(ctx, workflowID, "")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to cancel: %v\n", err)
		return 1
	}

	if *force {
		fmt.Printf("terminated: %s\n", workflowID)
	} else {
		fmt.Printf("cancel requested: %s\n", workflowID)
		fmt.Println("research children will stop with the parent")
		fmt.Printf("check: go run ./cmd/client status %s\n", workflowID)
	}
	return 0
}

func fetchAndPrint(c client.Client, workflowID string) int {
	we := c.GetWorkflow(context.Background(), workflowID, "")
	var result types.PipelineOutput
	if err := we.Get(context.Background(), &result); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline failed: %v\n", err)
		return 1
	}
	printResult(result)
	return 0
}

func printResult(result types.PipelineOutput) {
	fmt.Println("\n=== REPORT ===")
	if result.Report == "" {
		fmt.Println("(no report)")
	} else {
		fmt.Println(result.Report)
	}
	fmt.Println("\nstatus:", result.Status)
}
