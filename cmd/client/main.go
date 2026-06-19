package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jedrok/delver/internal/config"
	"github.com/jedrok/delver/internal/pipeline"
	"github.com/jedrok/delver/internal/types"
	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
)

func main() {

	if len(os.Args) < 2 {
		log.Fatal("usage: go run cmd/client/main.go \"put question here\"")
	}
	question := os.Args[1]

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

	workflowID := fmt.Sprintf("delver-%d", time.Now().Unix())
	opts := client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: cfg.TaskQueue,
	}

	log.Printf("starting pipeline: %s\n", workflowID)
	log.Printf("TemporalHost=%q", cfg.TemporalHost)
	log.Printf("TaskQueue=%q", cfg.TaskQueue)

	we, err := c.ExecuteWorkflow(context.Background(), opts,
		pipeline.ResearchPipelineWorkflow,
		types.PipelineInput{Question: question},
	)
	if err != nil {
		log.Fatalf("failed to start workflow: %v", err)
	}

	log.Println("waiting for result... (this may take a minute or two)")

	var result types.PipelineOutput
	err = we.Get(context.Background(), &result)
	if err != nil {
		log.Fatalf("pipeline failed: %v", err)
	}

	fmt.Println("\n=== REPORT ===")
	fmt.Println(result.Report)
	fmt.Println("\nstatus:", result.Status)
}
