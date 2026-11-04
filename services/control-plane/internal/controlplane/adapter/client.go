package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
)

type Client struct {
	BaseURL string
	// Token is the secret the adapter requires on /runs (FORGE_ADAPTER_TOKEN).
	Token string
	// CallbackToken gives a run's callback token; the adapter presents it
	// on that run's checkpoints and completion (Phase 13.1).
	CallbackToken func(runID string) string
}

type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type StartRunRequest struct {
	RunID          string             `json:"runId"`
	CallbackRunID  string             `json:"callbackRunId,omitempty"`
	ContainerName  string             `json:"containerName"`
	WorktreePath   string             `json:"worktreePath"`
	Role           string             `json:"role,omitempty"`
	Task           string             `json:"task"`
	SystemPrompt   string             `json:"systemPrompt"`
	Tools          []agent.ToolSchema `json:"tools,omitempty"` // TODO: rename to ToolSchema `json:"tools"`
	SubmissionTool string             `json:"submissionTool,omitempty"`
	CallbackURL    string             `json:"callbackUrl"` // ignored by the adapter since 13.1: it reports to its configured control plane
	CallbackToken  string             `json:"callbackToken,omitempty"`
	Provider       string             `json:"provider"`
	Model          string             `json:"model"`
	MaxSteps       int                `json:"maxSteps"`
	MaxRetries     int                `json:"maxRetries"`
	WallClockMS    int                `json:"wallClockMs"`
	Checkpoint     any                `json:"checkpoint,omitempty"`
	Diagnostics    string             `json:"diagnostics,omitempty"`
}

type StartRunResponse struct {
	Accepted bool `json:"accepted"`
}

func (c *Client) StartRun(ctx context.Context, req *StartRunRequest) (*StartRunResponse, error) {
	if c.CallbackToken != nil {
		id := req.CallbackRunID
		if id == "" {
			id = req.RunID
		}
		req.CallbackToken = c.CallbackToken(id)
	}
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/runs", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("adapter: HTTP 401: FORGE_ADAPTER_TOKEN differs between the control plane and the adapter")
	}
	if resp.StatusCode != 202 && resp.StatusCode != 200 {
		return nil, fmt.Errorf("adapter: HTTP %d", resp.StatusCode)
	}
	var out StartRunResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return &out, nil
}

// CancelRun asks the adapter to stop a run at its next step. A run the
// adapter no longer knows (404) is already stopped.
func (c *Client) CancelRun(ctx context.Context, runID string) error {
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/runs/"+runID+"/cancel", nil)
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 404 {
		return fmt.Errorf("adapter cancel: HTTP %d", resp.StatusCode)
	}
	return nil
}
