package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type EvalCase struct {
	ID          string   `json:"id"`
	FixtureRepo string   `json:"fixtureRepo"`
	Requirement string   `json:"requirement"`
	Acceptance  []string `json:"acceptance"`
	DiffScope   []string `json:"diffScope,omitempty"`
}

type Config struct {
	ControlPlane string
	SuiteDir     string
	Provider     string
	Model        string
	Timeout      time.Duration
	MaxSteps     int
}

func main() {
	cfg := Config{
		ControlPlane: envOr("CONTROL_PLANE_URL", "http://localhost:8080"),
		SuiteDir:     envOr("EVAL_SUITE_DIR", "tests/evaluations"),
		Provider:     envOr("EVAL_PROVIDER", "ollama"),
		Model:        envOr("EVAL_MODEL", os.Getenv("MODEL_NAME")),
		Timeout:      parseDur(envOr("EVAL_TIMEOUT", "15m"), 15*time.Minute),
		MaxSteps:     parseInt(envOr("EVAL_MAX_STEPS", "25"), 25),
	}
	if cfg.Model == "" {
		log.Fatal("EVAL_MODEL (or MODEL_NAME) is required")
	}

	matches, _ := filepath.Glob(filepath.Join(cfg.SuiteDir, "*.json"))
	if len(matches) == 0 {
		log.Fatalf("no eval cases found in %s", cfg.SuiteDir)
	}

	var pass, fail int
	started := time.Now()
	for _, path := range matches {
		c, err := loadCase(path)
		if err != nil {
			log.Printf("[%s] load error: %v", path, err)
			fail++
			continue
		}
		ok, err := runCase(cfg, c)
		if err != nil {
			log.Printf("[%s] %v", c.ID, err)
			fail++
			continue
		}
		if ok {
			pass++
		} else {
			fail++
		}
	}

	elapsed := time.Since(started)
	fmt.Printf("\n=== eval summary ===\n")
	fmt.Printf("model:   %s (%s)\n", cfg.Model, cfg.Provider)
	fmt.Printf("passed:  %d\n", pass)
	fmt.Printf("failed:  %d\n", fail)
	fmt.Printf("elapsed: %s\n", elapsed.Round(time.Second))
	if fail > 0 {
		os.Exit(1)
	}
}

func runCase(cfg Config, c EvalCase) (bool, error) {
	fmt.Printf("\n--- %s ---\n", c.ID)

	absRepo, err := filepath.Abs(c.FixtureRepo)
	if err != nil {
		return false, err
	}
	started := time.Now()

	taskID, err := postTask(cfg, absRepo, c)
	if err != nil {
		return false, err
	}
	fmt.Printf("  task: %s\n", taskID)

	runID, err := dispatchRun(cfg, taskID)
	if err != nil {
		return false, err
	}
	fmt.Printf("  run:  %s\n", runID)

	status, elapsed := waitTerminal(cfg, taskID, cfg.Timeout)
	passed := status == "REVIEW_PENDING" || status == "APPROVED"

	result := map[string]any{
		"caseId":     c.ID,
		"taskId":     taskID,
		"runId":      runID,
		"provider":   cfg.Provider,
		"model":      cfg.Model,
		"passed":     passed,
		"status":     status,
		"startedAt":  started,
		"finishedAt": time.Now(),
		"durationMs": int(elapsed / time.Millisecond),
	}
	if !passed {
		result["reason"] = "terminal status: " + status
	}
	_ = postEvalResult(cfg, result)

	if passed {
		fmt.Printf("  PASS  (%s)\n", elapsed.Round(time.Second))
	} else {
		fmt.Printf("  FAIL  status=%s  (%s)\n", status, elapsed.Round(time.Second))
	}
	return passed, nil
}

func postTask(cfg Config, repo string, c EvalCase) (string, error) {
	body := map[string]any{
		"requirement": c.Requirement,
		"acceptance":  c.Acceptance,
		"repoPath":    repo,
		"baseCommit":  "HEAD",
		"diffScope":   c.DiffScope,
		"budget":      map[string]int{"maxSteps": cfg.MaxSteps, "maxRetries": 2, "wallClockMs": 900000},
	}
	var out map[string]any
	if err := postJSON(cfg.ControlPlane+"/tasks", body, &out); err != nil {
		return "", err
	}
	return out["id"].(string), nil
}

func dispatchRun(cfg Config, taskID string) (string, error) {
	var out map[string]any
	err := postJSON(cfg.ControlPlane+"/tasks/"+taskID+"/runs",
		map[string]string{"provider": cfg.Provider, "model": cfg.Model}, &out)
	if err != nil {
		return "", err
	}
	return out["runId"].(string), nil
}

func waitTerminal(cfg Config, taskID string, timeout time.Duration) (string, time.Duration) {
	deadline := time.Now().Add(timeout)
	start := time.Now()
	lastStatus := ""
	for time.Now().Before(deadline) {
		status := getTaskStatus(cfg.ControlPlane, taskID)
		if status != lastStatus {
			fmt.Printf("  [%s] %s\n", time.Since(start).Round(time.Second), status)
			lastStatus = status
		}
		switch status {
		case "REVIEW_PENDING", "APPROVED", "BLOCKED", "REJECTED", "MERGED":
			return status, time.Since(start)
		}
		time.Sleep(2 * time.Second)
	}
	return lastStatus, time.Since(start)
}

func getTaskStatus(cp, taskID string) string {
	resp, err := http.Get(cp + "/tasks/" + taskID)
	if err != nil {
		return "UNKNOWN"
	}
	defer resp.Body.Close()
	var t map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&t)
	if s, ok := t["status"].(string); ok {
		return s
	}
	return "UNKNOWN"
}

func postEvalResult(cfg Config, r map[string]any) error {
	var out map[string]any
	return postJSON(cfg.ControlPlane+"/evals/results", r, &out)
}

func postJSON(url string, in, out any) error {
	buf, _ := json.Marshal(in)
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func loadCase(path string) (EvalCase, error) {
	var c EvalCase
	buf, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(buf, &c); err != nil {
		return c, err
	}
	if c.ID == "" {
		return c, fmt.Errorf("missing id")
	}
	return c, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func parseDur(s string, d time.Duration) time.Duration {
	if v, err := time.ParseDuration(s); err == nil {
		return v
	}
	return d
}
func parseInt(s string, d int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return d
}
