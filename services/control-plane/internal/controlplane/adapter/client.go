package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type Client struct{ BaseURL string }

type StartRunRequest struct {
	RunID         string         `json:"runId"`
	ContainerName string         `json:"containerName"`
	WorktreePath  string         `json:"worktreePath"`
	Task          string         `json:"task"`
	SystemPrompt  string         `json:"systemPrompt"`
	CallbackURL   string         `json:"callbackUrl"`
	Provider      string         `json:"provider"`
	Model         string         `json:"model"`
	MaxSteps      int            `json:"maxSteps"`
	MaxRetries    int            `json:"maxRetries"`
	WallClockMS   int            `json:"wallClockMs"`
	Checkpoint    any            `json:"checkpoint,omitempty"`
}

type StartRunResponse struct{ Accepted bool `json:"accepted"` }

func (c *Client) StartRun(ctx context.Context, req *StartRunRequest) (*StartRunResponse, error) {
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/runs", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 && resp.StatusCode != 200 {
		return nil, fmt.Errorf("adapter: HTTP %d", resp.StatusCode)
	}
	var out StartRunResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return &out, nil
}