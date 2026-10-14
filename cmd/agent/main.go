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
	if env("WEBCODEX_DEBUG", env("WEBCODEX_VERBOSE", "0")) == "0" {
		log.SetFlags(0)
	}
	if nativeHostInvocation(os.Args) {
		if err := runBrowserNativeHost(os.Args[1]); err != nil {
			log.Printf("native browser host: %v", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "--install-browser-host" || os.Args[1] == "--remove-browser-host") {
		if err := installBrowserNativeHost(os.Args[1] == "--remove-browser-host"); err != nil {
			log.Fatal(err)
		}
		return
	}
	gateURL := strings.TrimRight(env("WEBCODEX_GATE_URL", ""), "/")
	token := env("WEBCODEX_AGENT_TOKEN", "")
	if gateURL == "" || token == "" {
		log.Fatal("WEBCODEX_GATE_URL and WEBCODEX_AGENT_TOKEN are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{}
	var roots string
	for ctx.Err() == nil {
		var err error
		roots, err = loadAllowedRoots(ctx, client, gateURL, token)
		if err == nil {
			break
		}
		log.Printf("Не удалось получить настройки агента; пробую снова: %s", compactString(err.Error(), 180))
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if ctx.Err() != nil {
		return
	}
	executor, err := newNativeExecutor(roots)
	if err != nil {
		log.Fatalf("initialize local executor: %v", err)
	}
	defer executor.Close()
	if executor.verbose {
		log.Printf("agent ready: debug=true log_dir=%q", executor.logDir)
	} else {
		log.Print("Агент готов. Подробная диагностика: WEBCODEX_DEBUG=1.")
	}

	for ctx.Err() == nil {
		if err := streamOnce(ctx, client, gateURL, token, executor); err != nil && ctx.Err() == nil {
			log.Printf("⚠ Связь с Gate прервана; переподключаюсь. %s", compactString(err.Error(), 120))
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}
