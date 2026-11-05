package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Package managers of a Node project, told apart by its lockfile.
const (
	NPM  = "npm"
	PNPM = "pnpm"
	Yarn = "yarn"
)

// Project is a buildable project found in a repository: at its top level or,
// when nothing above holds it, in a folder such as app/. Dir is relative to
// the repository's top; "." is the top itself.
type Project struct {
	Dir string
	Go  bool   // go.mod; takes precedence over a package.json beside it
	PM  string // Node only
	// HasTest: a Go module, or a package.json with a real test script.
	HasTest bool
}

// projectDepth bounds how far down a project is looked for: a/b/c.
const projectDepth = 3

// Never projects of this repository's own: dependencies, build output, and
// sample projects that may fail on purpose.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
	"testdata": true, "fixtures": true, "__fixtures__": true, "examples": true,
}

// Projects finds the projects in a checkout. A project found stops the search
// below it: its own tooling covers any packages inside.
func Projects(root string) []Project {
	var out []Project
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		dir := filepath.Join(root, rel)
		if p, ok := projectIn(dir); ok {
			p.Dir = rel
			out = append(out, p)
			return
		}
		if depth == projectDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && !skipDirs[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
				walk(filepath.Join(rel, e.Name()), depth+1)
			}
		}
	}
	walk(".", 0)
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

func projectIn(dir string) (Project, bool) {
	if exists(filepath.Join(dir, "go.mod")) {
		return Project{Go: true, HasTest: true}, true
	}
	if !exists(filepath.Join(dir, "package.json")) {
		return Project{}, false
	}
	p := Project{PM: NPM, HasTest: hasTestScript(filepath.Join(dir, "package.json"))}
	switch {
	case exists(filepath.Join(dir, "pnpm-lock.yaml")):
		p.PM = PNPM
	case exists(filepath.Join(dir, "yarn.lock")):
		p.PM = Yarn
	}
	return p, true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasTestScript: npm init's placeholder only fails, so it doesn't count.
func hasTestScript(path string) bool {
	buf, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(buf, &pkg) != nil {
		return false
	}
	test := pkg.Scripts["test"]
	return test != "" && !strings.Contains(test, "no test specified")
}

// shellDir quotes a project's folder for sh.
func shellDir(dir string) string {
	return "'" + strings.ReplaceAll(dir, "'", `'\''`) + "'"
}

// ForEachProject is a script that runs each project's command in its folder,
// runs them all even after a failure, and fails if any did. Only a project in
// a subfolder gets a heading.
func ForEachProject(projects []Project, command func(Project) string) string {
	var b strings.Builder
	b.WriteString("fail=0\n")
	for _, p := range projects {
		if p.Dir != "." {
			fmt.Fprintf(&b, "echo '== '%s\n", shellDir(p.Dir))
		}
		fmt.Fprintf(&b, "(cd %s && %s) || fail=1\n", shellDir(p.Dir), command(p))
	}
	b.WriteString("exit $fail\n")
	return b.String()
}

// pnpmStore is where the fetch step finds pnpm's packages. Most of a real
// project's install is downloading them, and every workspace repeats it.
const pnpmStore = "/pnpm-store"

// packageStoreArgs mounts a store kept between workspaces, for the fetch step
// only: it runs no scripts, and nothing an agent runs ever sees the store.
func packageStoreArgs(projects []Project) []string {
	if !slices.ContainsFunc(projects, func(p Project) bool { return p.PM == PNPM }) {
		return nil
	}
	dir := filepath.Join(os.TempDir(), "forge-pnpm-store")
	if cache, err := os.UserCacheDir(); err == nil {
		dir = filepath.Join(cache, "forge", "pnpm-store")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = os.TempDir() // still somewhere it can write
	}
	return []string{"-v", dir + ":" + pnpmStore}
}

// fetchCommand gets a project's packages without running any of their scripts.
func fetchCommand(p Project) string {
	switch {
	case p.Go:
		return "go mod download"
	case p.PM == PNPM:
		const pnpm = "pnpm install --ignore-scripts --reporter=append-only --store-dir " + pnpmStore
		return pnpm + " --frozen-lockfile || " + pnpm + " --no-frozen-lockfile"
	case p.PM == Yarn:
		const yarn = "yarn install --ignore-scripts --cache-folder /tmp/.yarn"
		return yarn + " --frozen-lockfile || " + yarn
	}
	return "npm install --no-audit --no-fund --ignore-scripts"
}

// scriptsCommand runs the install scripts fetchCommand held back.
func scriptsCommand(p Project) string {
	if p.Go {
		return ":"
	}
	rebuild := "npm rebuild --no-audit --no-fund"
	if p.PM == PNPM {
		rebuild = "pnpm rebuild --store-dir /tmp/.pnpm-store"
	}
	return rebuild + ` && for s in preinstall install postinstall prepare; do npm run --if-present "$s" || exit 1; done`
}
