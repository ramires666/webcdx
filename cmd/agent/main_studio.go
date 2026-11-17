//go:build aistudio

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"webcodex/internal/buildinfo"
)

func main() {
	log.Printf("AIX local agent version=%s", buildinfo.Version)
	if len(os.Args) > 2 || len(os.Args) == 2 && os.Args[1] != "--local" {
		log.Fatal("Usage: webcodex-agent.exe [--local]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tasks := startConfiguredTasks()
	if tasks != nil {
		defer tasks.close()
	}
	closeLocal, err := startStudioLocalAccess(ctx, tasks)
	if err != nil {
		log.Fatalf("start local AI Studio: %v", err)
	}
	defer closeLocal()
	log.Print("Google AI Studio local agent ready")
	<-ctx.Done()
}
