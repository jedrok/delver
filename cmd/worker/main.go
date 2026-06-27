package main

import (
	"log"

	"github.com/jedrok/delver/internal/activities"
	"github.com/jedrok/delver/internal/config"
	"github.com/jedrok/delver/internal/pipeline"
	"github.com/jedrok/delver/internal/workflows"
	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {

	// load env variables
	if err := godotenv.Load(); err != nil {
		log.Printf("warning: .env file not found %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// connect to temporal server
	c, err := client.Dial(client.Options{
		HostPort: cfg.TemporalHost,
	})
	if err != nil {
		log.Fatalf("failed to connect to temporal: %v", err)
	}
	defer c.Close()

	llmActivities := activities.NewLLMActivities(cfg)
	toolActivities := activities.NewToolActivities()

	// create the worker
	w := worker.New(c, cfg.TaskQueue, worker.Options{})

	// register workflows
	w.RegisterWorkflow(pipeline.ResearchPipelineWorkflow)
	w.RegisterWorkflow(workflows.AgentLoopWorkflow)
	w.RegisterWorkflow(workflows.ApprovalGateWorkflow)

	// register activities
	w.RegisterActivity(llmActivities)
	w.RegisterActivity(toolActivities)

	log.Println("worker starting, task queue:", cfg.TaskQueue)

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalf("worker stopped with error: %v", err)
	}
}
