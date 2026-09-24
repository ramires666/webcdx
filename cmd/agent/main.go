// Command agent connects a local file and process executor to the public gate.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	gateURL := strings.TrimRight(env("WEBCODEX_GATE_URL", ""), "/")
	token := env("WEBCODEX_AGENT_TOKEN", "")
	if gateURL == "" || token == "" {
		log.Fatal("WEBCODEX_GATE_URL and WEBCODEX_AGENT_TOKEN are required")
	}
	executor, err := newNativeExecutor()
	if err != nil {
		log.Fatalf("initialize local executor: %v", err)
	}
	defer executor.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{}
	for ctx.Err() == nil {
		if err := streamOnce(ctx, client, gateURL, token, executor); err != nil && ctx.Err() == nil {
			log.Printf("stream: %v", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}
