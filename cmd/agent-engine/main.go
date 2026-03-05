package main

import (
	"fmt"
	"log/slog"
	"os"
)

func main() {
	jobID := os.Getenv("JOB_ID")
	prompt := os.Getenv("PROMPT")

	slog.Info("agent-engine started",
		"job_id", jobID,
		"prompt", prompt,
	)

	fmt.Println("hello from ECS")

	slog.Info("agent-engine finished", "job_id", jobID)
}
