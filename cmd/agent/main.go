// Command agent keeps an outbound connection to the gate and proxies calls to Codex MCP.
package main

import (
	"context"
	"fmt"
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
	model := env("WEBCODEX_DEFAULT_MODEL", "gpt-5.6-sol")
	reasoning := env("WEBCODEX_DEFAULT_REASONING_EFFORT", "high")

	defaultArgs := []string{
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="danger-full-access"`,
		"-c", fmt.Sprintf(`model=%q`, model),
		"-c", fmt.Sprintf(`model_reasoning_effort=%q`, reasoning),
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

	codexCliArgs := []string{
		"--dangerously-bypass-approvals-and-sandbox",
		"-c", fmt.Sprintf(`model=%q`, model),
		"-c", fmt.Sprintf(`model_reasoning_effort=%q`, reasoning),
		"mcp-server",
	}
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
	mode := strings.ToLower(env("WEBCODEX_MODE", "native"))

	if gateURL == "" || token == "" {
		log.Fatal("WEBCODEX_GATE_URL and WEBCODEX_AGENT_TOKEN are required")
	}

	var runner mcpRunner
	if mode == "codex" {
		binary, args := findCodexMCP()
		if fileExists(binary) || isCommandAvailable(binary) {
			log.Printf("Starting agent with worker engine (%s)...", binary)
			mcp, err := startMCP(context.Background(), binary, args)
			if err != nil {
				log.Fatalf("start worker engine (%s): %v", binary, err)
			} else if err := mcp.initialize(context.Background()); err != nil {
				log.Fatalf("initialize worker engine: %v", err)
			} else {
				log.Printf("Worker engine connected and ready (all autonomous agent features active).")
				runner = mcp
			}
		} else {
			log.Fatalf("Worker engine binary not found: %s", binary)
		}
	}

	if runner == nil {
		log.Printf("Starting agent in NATIVE DIRECT mode (zero external limits, pure local execution, 0 tokens spent)")
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
