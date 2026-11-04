// Command eval scores how well the agents do real work: each case in the suite
// runs through a running Forge against a fresh copy of its fixture, then the
// result is checked independently of Forge's own gates and reviewers. Results
// go to the console's Evals page. `make eval` sets it up; it spends real money
// with a paid model, within EVAL_TASK_MAX_COST_USD and EVAL_MAX_COST_USD.
//
//	eval [case id substring ...]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type EvalCase struct {
	ID          string   `json:"id"`
	FixtureRepo string   `json:"fixtureRepo"`
	Requirement string   `json:"requirement"`
	Acceptance  []string `json:"acceptance"`
	DiffScope   []string `json:"diffScope,omitempty"`
	Checks      Checks   `json:"checks"`
}

// Checks is what makes a case pass, beyond reaching review.
type Checks struct {
	// Commands run in the delivered tree, offline in a worker container;
	// each must exit 0. The agent never sees them.
	Commands []string `json:"commands,omitempty"`
	// OnlyChanged: every changed file must match one of these (path.Match
	// patterns; a pattern ending in / covers everything under it).
	OnlyChanged []string `json:"onlyChanged,omitempty"`
}

type Config struct {
	ControlPlane string
	SuiteDir     string
	Provider     string
	Model        string
	Image        string
	Project      string
	Timeout      time.Duration
	MaxSteps     int
	TaskMaxCost  float64
	SuiteMaxCost float64
}

func main() {
	cfg := Config{
		ControlPlane: envOr("CONTROL_PLANE_URL", "http://localhost:8085"),
		SuiteDir:     envOr("EVAL_SUITE_DIR", "tests/evaluations"),
		Provider:     envOr("EVAL_PROVIDER", envOr("MODEL_PROVIDER", "ollama")),
		Model:        envOr("EVAL_MODEL", os.Getenv("MODEL_NAME")),
		Image:        envOr("WORKER_IMAGE", "forge-worker:dev"),
		Project:      os.Getenv("FORGE_PROJECT"),
		Timeout:      parseDur(envOr("EVAL_TIMEOUT", "15m"), 15*time.Minute),
		MaxSteps:     parseInt(envOr("EVAL_MAX_STEPS", "25"), 25),
		TaskMaxCost:  parseFloat(envOr("EVAL_TASK_MAX_COST_USD", "0.5"), 0.5),
		SuiteMaxCost: parseFloat(envOr("EVAL_MAX_COST_USD", "2"), 2),
	}
	if cfg.Model == "" {
		log.Fatal("EVAL_MODEL (or MODEL_NAME) is required")
	}

	matches, _ := filepath.Glob(filepath.Join(cfg.SuiteDir, "*.json"))
	matches = selectCases(matches, os.Args[1:])
	if len(matches) == 0 {
		log.Fatalf("no eval cases found in %s", cfg.SuiteDir)
	}
	if cfg.Project == "" {
		id, err := ensureProject(cfg, "evals")
		if err != nil {
			log.Fatalf("the evals project: %v", err)
		}
		cfg.Project = id
	}

	var pass, fail, notRun int
	var spent float64
	started := time.Now()
	for _, p := range matches {
		if spent >= cfg.SuiteMaxCost {
			fmt.Printf("\nspent $%.4f, the suite's cap is $%.2f: not running %s\n", spent, cfg.SuiteMaxCost, filepath.Base(p))
			notRun++
			continue
		}
		c, err := loadCase(p)
		if err != nil {
			log.Printf("[%s] load error: %v", p, err)
			fail++
			continue
		}
		ok, cost, err := runCase(cfg, c)
		spent += cost
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

	fmt.Printf("\n=== eval summary ===\n")
	fmt.Printf("model:   %s (%s)\n", cfg.Model, cfg.Provider)
	fmt.Printf("passed:  %d\n", pass)
	fmt.Printf("failed:  %d\n", fail)
	if notRun > 0 {
		fmt.Printf("not run: %d\n", notRun)
	}
	fmt.Printf("cost:    $%.4f by Forge's prices\n", spent)
	fmt.Printf("elapsed: %s\n", time.Since(started).Round(time.Second))
	if fail > 0 || notRun > 0 {
		os.Exit(1)
	}
}

func selectCases(paths, wanted []string) []string {
	if len(wanted) == 0 {
		return paths
	}
	var out []string
	for _, p := range paths {
		for _, w := range wanted {
			if strings.Contains(filepath.Base(p), w) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func runCase(cfg Config, c EvalCase) (passed bool, cost float64, err error) {
	fmt.Printf("\n--- %s ---\n", c.ID)
	started := time.Now()

	repo, err := freshRepo(c.FixtureRepo)
	if err != nil {
		return false, 0, err
	}
	repoURL := cfg.ControlPlane + "/projects/" + cfg.Project + "/repos"
	if err := postJSON(repoURL, map[string]string{"path": repo, "defaultTarget": "main"}, nil); err != nil {
		return false, 0, err
	}
	defer deleteURL(repoURL + "?" + url.Values{"path": {repo}}.Encode())

	taskID, err := postTask(cfg, repo, c)
	if err != nil {
		return false, 0, err
	}
	fmt.Printf("  task: %s\n", taskID)
	runID, err := dispatchRun(cfg, taskID)
	if err != nil {
		return false, 0, err
	}

	status, elapsed := waitTerminal(cfg, taskID, cfg.Timeout)
	cost = taskCost(cfg, taskID)

	var failures []string
	if status != "REVIEW_PENDING" {
		failures = append(failures, "ended at "+status+whyBlocked(cfg, taskID))
	} else {
		failures = checkResult(cfg, c, taskID, repo)
	}
	passed = len(failures) == 0

	result := map[string]any{
		"caseId":     c.ID,
		"taskId":     taskID,
		"runId":      runID,
		"provider":   cfg.Provider,
		"model":      cfg.Model,
		"passed":     passed,
		"status":     status,
		"reason":     strings.Join(failures, "; "),
		"startedAt":  started,
		"finishedAt": time.Now(),
		"durationMs": int(elapsed / time.Millisecond),
	}
	if err := postJSON(cfg.ControlPlane+"/evals/results", result, nil); err != nil {
		log.Printf("[%s] the result was not saved: %v", c.ID, err)
	}
	// Scored: release its workspaces. Nothing is ever delivered.
	if status == "REVIEW_PENDING" {
		_ = postJSON(cfg.ControlPlane+"/tasks/"+taskID+"/reject", map[string]string{"rationale": "eval case scored"}, nil)
	}

	if passed {
		fmt.Printf("  PASS  (%s, $%.4f)\n", elapsed.Round(time.Second), cost)
	} else {
		fmt.Printf("  FAIL  %s  (%s, $%.4f)\n", strings.Join(failures, "; "), elapsed.Round(time.Second), cost)
	}
	return passed, cost, nil
}

// freshRepo copies a fixture to a new git repository, so every run starts the same.
func freshRepo(fixture string) (string, error) {
	abs, err := filepath.Abs(fixture)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "forge-eval-")
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return "", err
	}
	if out, err := exec.Command("cp", "-R", abs+"/.", dir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("copy %s: %s", fixture, out)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=forge-eval", "-c", "user.email=forge-eval@localhost", "commit", "-q", "-m", "fixture"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %v: %s", args, out)
		}
	}
	return dir, nil
}

// checkResult scores the parent branch, the change a human would be shown.
func checkResult(cfg Config, c EvalCase, taskID, repo string) []string {
	var task struct {
		Branch string `json:"integrationBranch"`
		Base   string `json:"integrationBaseCommit"`
	}
	if err := getJSON(cfg.ControlPlane+"/tasks/"+taskID, &task); err != nil {
		return []string{"could not read the task: " + err.Error()}
	}
	out, err := exec.Command("git", "-C", repo, "diff", "--name-only", task.Base, task.Branch).Output()
	if err != nil {
		return []string{"could not read the change: " + err.Error()}
	}
	failures := outsideScope(strings.Fields(string(out)), c.Checks.OnlyChanged)
	if len(c.Checks.Commands) == 0 {
		return failures
	}

	tree, err := os.MkdirTemp("", "forge-eval-tree-")
	if err != nil {
		return append(failures, err.Error())
	}
	defer os.RemoveAll(tree)
	extract := fmt.Sprintf("git -C %q archive %q | tar -x -C %q", repo, task.Branch, tree)
	if out, err := exec.Command("sh", "-c", extract).CombinedOutput(); err != nil {
		return append(failures, fmt.Sprintf("could not extract the change: %s", out))
	}
	for _, command := range c.Checks.Commands {
		if msg, ok := runCheck(cfg.Image, tree, command); !ok {
			failures = append(failures, fmt.Sprintf("check failed: %s: %s", command, msg))
		}
	}
	return failures
}

// outsideScope lists the changed files no pattern covers, and an empty change.
func outsideScope(changed, patterns []string) []string {
	if len(changed) == 0 {
		return []string{"changed no files"}
	}
	var failures []string
	for _, f := range changed {
		if len(patterns) > 0 && !coveredBy(f, patterns) {
			failures = append(failures, "changed "+f+", which it had no reason to")
		}
	}
	return failures
}

func coveredBy(file string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") && strings.HasPrefix(file, p) {
			return true
		}
		if ok, _ := path.Match(p, file); ok {
			return true
		}
	}
	return false
}

// runCheck runs agent-written code, so it gets what a worker gets, read-only.
func runCheck(image, tree, command string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network=none", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--read-only", "--tmpfs", "/tmp:rw,size=256m", "-e", "HOME=/tmp",
		"-v", tree+":/workspace:ro", "--workdir", "/workspace", image, "sh", "-c", command).CombinedOutput()
	if err != nil {
		return failureLine(string(out)), false
	}
	return "", true
}

// failureLine picks what a failed check said: the error, not the end of its stack trace.
func failureLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for _, l := range lines {
		if strings.Contains(l, "Error") {
			return strings.TrimSpace(l)
		}
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func ensureProject(cfg Config, name string) (string, error) {
	var projects []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := getJSON(cfg.ControlPlane+"/projects", &projects); err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.Name == name {
			return p.ID, nil
		}
	}
	var created struct {
		ID string `json:"id"`
	}
	err := postJSON(cfg.ControlPlane+"/projects", map[string]string{"name": name}, &created)
	return created.ID, err
}

func postTask(cfg Config, repo string, c EvalCase) (string, error) {
	body := map[string]any{
		"projectId":    cfg.Project,
		"requirement":  c.Requirement,
		"acceptance":   c.Acceptance,
		"repoPath":     repo,
		"targetBranch": "main",
		"diffScope":    c.DiffScope,
		"budget":       map[string]any{"maxSteps": cfg.MaxSteps, "maxRetries": 2, "wallClockMs": 900000, "maxCostUsd": cfg.TaskMaxCost},
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
	run, _ := out["runId"].(string)
	return run, nil
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
		case "REVIEW_PENDING", "APPROVED", "BLOCKED", "REJECTED", "MERGED", "ESCALATED":
			return status, time.Since(start)
		}
		time.Sleep(2 * time.Second)
	}
	return lastStatus, time.Since(start)
}

func getTaskStatus(cp, taskID string) string {
	var t struct {
		Status string `json:"status"`
	}
	if err := getJSON(cp+"/tasks/"+taskID, &t); err != nil || t.Status == "" {
		return "UNKNOWN"
	}
	return t.Status
}

// whyBlocked: a parent's own reason only names its subtask, so report the
// subtask's too.
func whyBlocked(cfg Config, taskID string) string {
	reason := lastBlockedReason(cfg, taskID)
	var children []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = getJSON(cfg.ControlPlane+"/tasks/"+taskID+"/children", &children)
	for _, c := range children {
		if c.Status == "BLOCKED" {
			reason = lastBlockedReason(cfg, c.ID)
			break
		}
	}
	if reason == "" {
		return ""
	}
	return ": " + reason
}

func lastBlockedReason(cfg Config, taskID string) string {
	var events []struct {
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	_ = getJSON(cfg.ControlPlane+"/tasks/"+taskID+"/events", &events)
	reason := ""
	for _, e := range events {
		if e.To == "BLOCKED" {
			reason = e.Reason
		}
	}
	return reason
}

func taskCost(cfg Config, taskID string) float64 {
	var s struct {
		Task struct {
			CostUSD float64 `json:"costUsd"`
		} `json:"task"`
	}
	_ = getJSON(cfg.ControlPlane+"/tasks/"+taskID+"/spend", &s)
	return s.Task.CostUSD
}

// The API needs a signed-in caller (Phase 13.2): an API token.
func do(method, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if t := os.Getenv("FORGE_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return http.DefaultClient.Do(req)
}

func postJSON(url string, in, out any) error {
	buf, _ := json.Marshal(in)
	resp, err := do(http.MethodPost, url, buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		return fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(msg.String()))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func getJSON(url string, out any) error {
	resp, err := do(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func deleteURL(url string) {
	if resp, err := do(http.MethodDelete, url, nil); err == nil {
		resp.Body.Close()
	}
}

func loadCase(p string) (EvalCase, error) {
	var c EvalCase
	buf, err := os.ReadFile(p)
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
func parseFloat(s string, d float64) float64 {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return d
}
