package verification

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

func gate(t *testing.T, name string) Gate {
	t.Helper()
	for _, g := range DefaultPolicy() {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no gate %s", name)
	return Gate{}
}

func script(c Command) string { return c.Bin + " " + strings.Join(c.Args, " ") }

func TestTestGate(t *testing.T) {
	test := gate(t, "test")
	if _, run := test.Command(ProjectContext{}); run {
		t.Error("no project: skipped")
	}
	if _, run := test.Command(ProjectContext{Projects: []workspace.Project{{Dir: ".", PM: workspace.NPM}}}); run {
		t.Error("no test script: skipped, not a failure")
	}
	c, run := test.Command(ProjectContext{Projects: []workspace.Project{
		{Dir: "app", PM: workspace.PNPM, HasTest: true},
		{Dir: "web", PM: workspace.Yarn, HasTest: true},
		{Dir: "docs", PM: workspace.NPM},
		{Dir: "api", Go: true, HasTest: true},
	}})
	if !run {
		t.Fatal("skipped")
	}
	s := script(c)
	for _, want := range []string{"(cd 'app' && pnpm test)", "(cd 'web' && yarn test)", "(cd 'api' && go test ./...)"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}
	if strings.Contains(s, "'docs'") {
		t.Errorf("a project with no tests isn't run: %s", s)
	}
}

func TestTruncateOutput(t *testing.T) {
	if got := truncateOutput("short", 100); got != "short" {
		t.Error(got)
	}
	got := truncateOutput("start"+strings.Repeat("x", 1000)+"3 tests failed", 100)
	if !strings.HasPrefix(got, "start") || !strings.HasSuffix(got, "3 tests failed") || len(got) > 130 {
		t.Errorf("keeps both ends: %q", got)
	}
}

func TestBuildGate(t *testing.T) {
	build := gate(t, "build")
	if _, run := build.Command(ProjectContext{}); run {
		t.Error("no project: skipped")
	}
	c, run := build.Command(ProjectContext{Projects: []workspace.Project{{Dir: "app", PM: workspace.PNPM}, {Dir: "api", Go: true}}})
	if !run {
		t.Fatal("skipped")
	}
	s := script(c)
	if !strings.Contains(s, "cd 'app'") || !strings.Contains(s, "git diff --relative --name-only HEAD") || !strings.Contains(s, "(cd 'api' && go build ./...)") {
		t.Errorf("build: %s", s)
	}
}

func runDiffScope(t *testing.T, dir string, allow ...string) (string, bool) {
	t.Helper()
	for _, g := range DefaultPolicy() {
		if g.Name != "diff-scope" {
			continue
		}
		c, ok := g.Command(ProjectContext{DiffScopeAllow: allow})
		if !ok {
			t.Fatal("the gate did not run")
		}
		cmd := exec.Command(c.Bin, c.Args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err == nil
	}
	t.Fatal("no diff-scope gate")
	return "", false
}

func TestDiffScope(t *testing.T) {
	dir := t.TempDir()
	write := func(path string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git("init", "-q")
	write("app/feature/page.tsx")
	write("app/router.ts")
	write("outside.ts")
	git("add", ".")
	git("commit", "-qm", "base")

	write("app/feature/page.tsx") // unchanged
	write("app/feature/index.ts")
	write("app/feature/components/deep/Drawer.tsx")
	write("app/feature/hooks/use feature.ts")
	if out, ok := runDiffScope(t, dir, "app/feature/**"); !ok {
		t.Errorf("files at any depth under dir/** are in scope: %s", out)
	}
	write("app/router2.ts")
	if out, ok := runDiffScope(t, dir, "app/feature/**"); ok || !strings.Contains(out, "app/router2.ts") || strings.Contains(out, "Drawer") {
		t.Errorf("only the file outside is named: %s", out)
	}
	if out, ok := runDiffScope(t, dir, "app/feature/**", "app/router2.ts"); !ok {
		t.Errorf("an exact path is in scope: %s", out)
	}
	if _, ok := runDiffScope(t, dir, "app/feature/index.ts", "app/router2.ts"); ok {
		t.Error("exact paths don't cover other files")
	}
}
