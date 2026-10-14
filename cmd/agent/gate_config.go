package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"webcodex/internal/protocol"
)

func loadAllowedRoots(ctx context.Context, client *http.Client, gateURL, token string) (string, error) {
	if roots := strings.TrimSpace(os.Getenv("WEBCODEX_ALLOWED_ROOTS")); roots != "" {
		return roots, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateURL+"/agent/config", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent config status %s", resp.Status)
	}
	var config protocol.AgentConfig
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&config); err != nil {
		return "", err
	}
	if strings.TrimSpace(config.AllowedRoots) == "" {
		return "", fmt.Errorf("gate returned empty allowed roots")
	}
	return config.AllowedRoots, nil
}
