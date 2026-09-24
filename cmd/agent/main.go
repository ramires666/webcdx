// Command agent keeps an outbound connection to the gate and proxies calls to Codex MCP.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func findCodexMCP() (string, []string) {
	defaultArgs := []string{
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="danger-full-access"`,
	}

	if custom := env("WEBCODEX_CODEX_MCP_CMD", ""); custom != "" {
		parts := strings.Fields(custom)
		if len(parts) > 0 {
			return parts[0], parts[1:]
		}
	}

	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		candidates := []string{
			filepath.Join(exeDir, "codex-mcp-server.exe"),
			filepath.Join(exeDir, "codex-mcp-server"),
		}
		for _, c := range candidates {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				return c, defaultArgs
			}
		}
	}

	relCandidates := []string{
		"codex-mcp-server.exe",
		"codex-mcp-server",
		"third_party/codex/codex-rs/target/debug/codex-mcp-server.exe",
		"third_party/codex/codex-rs/target/debug/codex-mcp-server",
		"third_party/codex/codex-rs/target/release/codex-mcp-server.exe",
		"third_party/codex/codex-rs/target/release/codex-mcp-server",
	}
	for _, c := range relCandidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, defaultArgs
		}
	}

	codexCliArgs := []string{"--dangerously-bypass-approvals-and-sandbox", "mcp-server"}
	if p, err := exec.LookPath("codex"); err == nil {
		return p, codexCliArgs
	}
	if p, err := exec.LookPath("codex.exe"); err == nil {
		return p, codexCliArgs
	}

	if runtime.GOOS == "windows" {
		return "codex-mcp-server.exe", defaultArgs
	}
	return "third_party/codex/codex-rs/target/debug/codex-mcp-server", defaultArgs
}

func main() {
	gateURL := strings.TrimRight(env("WEBCODEX_GATE_URL", ""), "/")
	token := env("WEBCODEX_AGENT_TOKEN", "")
	mode := strings.ToLower(env("WEBCODEX_MODE", "auto"))

	if gateURL == "" || token == "" {
		log.Fatal("WEBCODEX_GATE_URL and WEBCODEX_AGENT_TOKEN are required")
	}

	var runner mcpRunner
	if mode == "codex" || mode == "auto" {
		binary, args := findCodexMCP()
		if fileExists(binary) || isCommandAvailable(binary) {
			log.Printf("Starting agent with worker engine (%s)...", binary)
			mcp, err := startMCP(context.Background(), binary, args)
			if err != nil {
				if mode == "codex" {
					log.Fatalf("start worker engine (%s): %v", binary, err)
				}
				log.Printf("failed to start worker engine (%s): %v, falling back to native direct mode", binary, err)
			} else if err := mcp.initialize(context.Background()); err != nil {
				if mode == "codex" {
					log.Fatalf("initialize worker engine: %v", err)
				}
				log.Printf("failed to initialize worker engine: %v, falling back to native direct mode", err)
			} else {
				log.Printf("Worker engine connected and ready (all autonomous agent features active).")
				runner = mcp
			}
		} else if mode == "codex" {
			log.Fatalf("Worker engine binary not found: %s", binary)
		}
	}

	if runner == nil {
		log.Printf("Starting agent in NATIVE DIRECT mode (zero external limits, pure local execution)")
		runner = newNativeExecutor()
	}

	client := &http.Client{}
	for {
		if err := streamOnce(context.Background(), client, gateURL, token, runner); err != nil {
			log.Printf("stream: %v", err)
			time.Sleep(time.Second)
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isCommandAvailable(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}
