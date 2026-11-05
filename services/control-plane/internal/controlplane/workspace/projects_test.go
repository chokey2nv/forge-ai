package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const withTest = `{"scripts":{"test":"vitest run"}}`

func TestProjects(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []Project
	}{
		{"nothing", map[string]string{"README.md": "x"}, nil},
		{"at the top", map[string]string{"package.json": withTest}, []Project{{Dir: ".", PM: NPM, HasTest: true}}},
		{"a subfolder with pnpm", map[string]string{"app/package.json": withTest, "app/pnpm-lock.yaml": "", "prototype/a.html": ""},
			[]Project{{Dir: "app", PM: PNPM, HasTest: true}}},
		{"yarn", map[string]string{"web/package.json": withTest, "web/yarn.lock": ""}, []Project{{Dir: "web", PM: Yarn, HasTest: true}}},
		{"several", map[string]string{"apps/web/package.json": withTest, "services/api/go.mod": "module x"},
			[]Project{{Dir: "apps/web", PM: NPM, HasTest: true}, {Dir: "services/api", Go: true, HasTest: true}}},
		{"go wins in the same folder", map[string]string{"go.mod": "module x", "package.json": withTest}, []Project{{Dir: ".", Go: true, HasTest: true}}},
		{"a project hides what's inside it", map[string]string{"package.json": withTest, "packages/a/package.json": withTest},
			[]Project{{Dir: ".", PM: NPM, HasTest: true}}},
		{"no test script, or npm init's placeholder", map[string]string{
			"a/package.json": `{}`, "b/package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`},
			[]Project{{Dir: "a", PM: NPM}, {Dir: "b", PM: NPM}}},
		{"not dependencies, output, samples, hidden folders or too deep", map[string]string{
			"node_modules/x/package.json": withTest, "dist/package.json": withTest, "tests/fixtures/x/package.json": withTest,
			".cache/package.json": withTest, "a/b/c/d/package.json": withTest}, nil},
		{"as deep as allowed", map[string]string{"a/b/c/package.json": withTest}, []Project{{Dir: "a/b/c", PM: NPM, HasTest: true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Projects(tree(t, c.files))
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("got %+v, want %+v", got[i], c.want[i])
				}
			}
		})
	}
}

func TestForEachProject(t *testing.T) {
	s := ForEachProject([]Project{{Dir: "."}, {Dir: "it's"}}, func(Project) string { return "run" })
	for _, want := range []string{"(cd '.' && run) || fail=1", `(cd 'it'\''s' && run) || fail=1`, `echo '== ''it'\''s'`, "exit $fail"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}
	if strings.Contains(s, "echo '== '.") || strings.Contains(s, "echo '== ''.'") {
		t.Errorf("no heading for the top: %s", s)
	}
}

func TestPackageManagerCommands(t *testing.T) {
	if got := fetchCommand(Project{PM: PNPM}); !strings.Contains(got, "pnpm install --ignore-scripts") || !strings.Contains(got, "--frozen-lockfile") {
		t.Error(got)
	}
	if got := fetchCommand(Project{PM: Yarn}); !strings.Contains(got, "yarn install --ignore-scripts") {
		t.Error(got)
	}
	if got := fetchCommand(Project{Go: true}); got != "go mod download" {
		t.Error(got)
	}
	if got := scriptsCommand(Project{PM: PNPM}); !strings.HasPrefix(got, "pnpm rebuild") {
		t.Error(got)
	}
}
