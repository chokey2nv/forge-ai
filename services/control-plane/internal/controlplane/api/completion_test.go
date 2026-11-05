package api

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/agent"
)

// A new agent must route by its definition alone: copying the repo's agents
// and adding a reviewer under a new name must need no Go changes.
func TestCompletionRoutesByAgentDefinition(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")

	agentsDir := t.TempDir()
	entries, err := os.ReadDir(filepath.Join(root, "agents"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(root, "agents", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentsDir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	extra := `---
name: security-reviewer
description: Reviews diffs for security defects.
version: 1
submission_tool: submit_review
tools:
  - read_file
  - git_diff
reviewers: []
---

You review diffs for security defects.
`
	if err := os.WriteFile(filepath.Join(agentsDir, "security-reviewer.md"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := agent.NewRegistry(agentsDir, filepath.Join(root, "skills"), nil)
	if err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := &Handlers{Registry: reg}

	for name, want := range map[string]completionKind{
		"head-engineer":     kindDecompose,
		"planner":           kindPlan,
		"reviewer":          kindReview,
		"security-reviewer": kindReview,
		"implementer":       kindWork,
		"qa-engineer":       kindWork,
		"not-registered":    kindWork,
	} {
		if got := h.completionKindOf(name); got != want {
			t.Errorf("completionKindOf(%q) = %s, want %s", name, got, want)
		}
	}

	var assignable []string
	for _, a := range h.assignableAgents() {
		assignable = append(assignable, a.Name)
	}
	sort.Strings(assignable)
	if got, want := strings.Join(assignable, ","), "frontend-engineer,implementer,qa-engineer"; got != want {
		t.Errorf("assignable agents = %s, want %s (reviewers, planners and the head engineer must not take subtasks)", got, want)
	}

	for kind := range map[completionKind]bool{kindWork: true, kindDecompose: true, kindPlan: true, kindReview: true} {
		if completionHandlers[kind] == nil {
			t.Errorf("no completion handler for kind %s", kind)
		}
	}
	for tool, kind := range kindBySubmissionTool {
		if _, ok := agent.ToolCatalog[tool]; !ok {
			t.Errorf("submission tool %s (kind %s) is not in the tool catalog", tool, kind)
		}
	}
}
