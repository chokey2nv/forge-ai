package verification

import (
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
