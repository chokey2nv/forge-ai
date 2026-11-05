package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func has(args []string, want ...string) bool {
	joined := "\x00" + strings.Join(args, "\x00") + "\x00"
	return strings.Contains(joined, "\x00"+strings.Join(want, "\x00")+"\x00")
}

func TestWorkerContainersAreUnprivileged(t *testing.T) {
	args := workerArgs("forge-worker-x", "run-x", "/tmp/wt", "img")
	for _, want := range [][]string{
		{"--network=none"}, {"--cap-drop=ALL"}, {"--security-opt=no-new-privileges"}, {"--read-only"},
		{"--tmpfs", "/tmp:rw,exec,nosuid,size=1g"}, {"-e", "HOME=/tmp"}, {"-v", "/tmp/wt:/workspace:rw"},
		{"img", "sleep", "infinity"},
	} {
		if !has(args, want...) {
			t.Errorf("worker args lack %v: %v", want, args)
		}
	}
	uid, _ := containerUser()
	if uid == 0 || !has(args, "--user") {
		t.Errorf("containers must not run as root: %v", args)
	}
	if args[len(args)-3] != "img" {
		t.Error("the image comes after every flag")
	}
}

// stubDocker records every docker call; each call's output comes from the
// script's case statement. Returns a reader of the calls so far.
func stubDocker(t *testing.T, cases string) func() []string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "docker.log")
	script := "#!/bin/sh\nprintf '%s\\036' \"$*\" >>" + log + "\ncase \"$*\" in\n" + cases + "\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		out, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSuffix(string(out), "\036"), "\036")
	}
}

func matching(calls []string, want string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c, want) {
			out = append(out, c)
		}
	}
	return out
}

func checkSandboxed(t *testing.T, c string) {
	t.Helper()
	for _, want := range []string{"--cap-drop=ALL", "--read-only", "--security-opt=no-new-privileges", "--user "} {
		if !strings.Contains(c, want) {
			t.Errorf("%q lacks %q", c, want)
		}
	}
}

const withDep = `{"scripts":{"test":"x","prepare":"evil"},"dependencies":{"a":"1.0.0"}}`

// A project with packages: fetched with the network and no scripts, then the
// packages' scripts with no network, into a volume workers mount read-only.
func TestSharedInstallBuildsOnce(t *testing.T) {
	depsReady.Clear()
	const missing = `"volume inspect"*) exit 1 ;;`
	calls := stubDocker(t, missing)
	repo := tree(t, map[string]string{"package.json": withDep, "package-lock.json": "{}"})

	mounts, err := installDeps(context.Background(), repo, "img", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	all := calls()
	fetch := matching(all, "npm install --no-audit --no-fund --ignore-scripts")
	if len(fetch) != 1 || !strings.Contains(fetch[0], "--network=bridge") {
		t.Fatalf("fetch step: %v", all)
	}
	checkSandboxed(t, fetch[0])
	if strings.Contains(fetch[0], "-v "+repo) {
		t.Errorf("the install must not see the repository: %s", fetch[0])
	}
	scripts := matching(all, "npm rebuild")
	if len(scripts) != 1 || !strings.Contains(scripts[0], "--network=none") || !strings.Contains(scripts[0], depsMarker) {
		t.Fatalf("scripts step: %v", all)
	}
	checkSandboxed(t, scripts[0])
	own := matching(all, "forge-setup-own-")
	if len(own) == 0 || !strings.Contains(strings.Join(own, " "), "--network=none") || !strings.Contains(strings.Join(own, " "), ":/workspace/node_modules:ro") {
		t.Errorf("the project's own scripts run offline, with the volume read-only: %v", own)
	}
	args := strings.Join(mounts, " ")
	if !strings.Contains(args, ":/workspace/node_modules:ro") || !strings.Contains(args, "/workspace/node_modules/.vite:rw") {
		t.Errorf("mounts: %s", args)
	}
	if !has(workerArgs("w", "r", repo, "img", mounts...), mounts[0], mounts[1]) {
		t.Error("the worker gets the mounts")
	}

	// A second workspace: the volume exists and is known finished.
	calls2 := stubDocker(t, "")
	if _, err := installDeps(context.Background(), repo, "img", time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := matching(calls2(), "npm install"); len(got) != 0 {
		t.Errorf("reinstalled: %v", got)
	}
}

func TestUnfinishedVolumeIsRebuilt(t *testing.T) {
	depsReady.Clear()
	calls := stubDocker(t, `*"test -f"*) exit 1 ;;`) // the volume exists, with no marker
	repo := tree(t, map[string]string{"package.json": withDep})
	if _, err := installDeps(context.Background(), repo, "img", time.Minute); err != nil {
		t.Fatal(err)
	}
	all := calls()
	if len(matching(all, "volume rm forge-deps-")) != 1 || len(matching(all, "npm install")) != 1 {
		t.Errorf("want it removed and rebuilt: %v", all)
	}
}

func TestFailedVolumeFallsBackToTheWorkspace(t *testing.T) {
	depsReady.Clear()
	calls := stubDocker(t, `"volume inspect"*) exit 1 ;;
*"--name forge-setup"*) exit 0 ;;
*"npm install"*) exit 1 ;;`)
	repo := tree(t, map[string]string{"package.json": withDep})
	mounts, err := installDeps(context.Background(), repo, "img", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 0 {
		t.Errorf("no volume to mount: %v", mounts)
	}
	all := calls()
	if len(matching(all, "volume rm forge-deps-")) != 1 {
		t.Errorf("the failed volume is removed: %v", all)
	}
	if len(matching(all, "--name forge-setup-forge")) == 0 && len(matching(all, "--name forge-setup-")) == 0 {
		t.Errorf("it installs in the workspace instead: %v", all)
	}
}

func TestWhatInstallsPerWorkspace(t *testing.T) {
	depsReady.Clear()
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"npm workspaces", map[string]string{"package.json": `{"workspaces":["p/*"],"dependencies":{"a":"1"}}`}},
		{"pnpm workspace", map[string]string{"package.json": withDep, "pnpm-workspace.yaml": "packages:\n  - p/*\n"}},
		{"a local dependency", map[string]string{"package.json": `{"dependencies":{"a":"file:../a"}}`}},
		{"a Go module", map[string]string{"go.mod": "module x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := stubDocker(t, "")
			mounts, err := installDeps(context.Background(), tree(t, c.files), "img", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			all := calls()
			if len(mounts) != 0 || len(matching(all, "volume create")) != 0 || len(matching(all, "--network=bridge")) != 1 {
				t.Errorf("mounts %v, calls %v", mounts, all)
			}
			checkSandboxed(t, matching(all, "--network=bridge")[0])
		})
	}
	t.Run("pnpm-workspace.yaml with only settings is shareable", func(t *testing.T) {
		repo := tree(t, map[string]string{"package.json": withDep, "pnpm-workspace.yaml": "allowBuilds:\n  esbuild: true\n"})
		p := Project{Dir: ".", PM: NPM}
		m, _ := readManifest(repo)
		if !shareable(repo, p, m) {
			t.Error("not shareable")
		}
	})
}

func TestNoPackagesNoContainers(t *testing.T) {
	calls := stubDocker(t, "")
	mounts, err := installDeps(context.Background(), tree(t, map[string]string{"package.json": `{"scripts":{"test":"x"}}`}), "img", time.Minute)
	if err != nil || len(mounts) != 0 {
		t.Fatal(err, mounts)
	}
	if got := calls(); len(got) > 1 || (len(got) == 1 && got[0] != "") {
		t.Errorf("no containers needed: %v", got)
	}
	// Its own install hook still runs, offline.
	calls = stubDocker(t, "")
	if _, err := installDeps(context.Background(), tree(t, map[string]string{"package.json": `{"scripts":{"postinstall":"x"}}`}), "img", time.Minute); err != nil {
		t.Fatal(err)
	}
	own := matching(calls(), "--network=none")
	if len(own) != 1 || !strings.Contains(own[0], "postinstall") {
		t.Errorf("own scripts: %v", own)
	}
	checkSandboxed(t, own[0])
}

func TestVolumeNameFollowsWhatDecidesTheInstall(t *testing.T) {
	p := Project{Dir: "app", PM: PNPM}
	name := func(files map[string]string, proj Project, image string) string {
		n, err := depsVolumeName(tree(t, files), proj, image)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	base := map[string]string{"package.json": withDep, "pnpm-lock.yaml": "a"}
	first := name(base, p, "img1")
	if first != name(base, p, "img1") {
		t.Error("the same inputs must give the same volume")
	}
	if first == name(map[string]string{"package.json": withDep, "pnpm-lock.yaml": "b"}, p, "img1") {
		t.Error("a changed lockfile needs a new volume")
	}
	if first == name(map[string]string{"package.json": `{"dependencies":{"b":"1"}}`, "pnpm-lock.yaml": "a"}, p, "img1") {
		t.Error("a changed package.json needs a new volume")
	}
	if first == name(base, p, "img2") {
		t.Error("a new image needs a new volume")
	}
	if first == name(base, Project{Dir: "web", PM: PNPM}, "img1") {
		t.Error("another folder needs its own volume")
	}
}

func TestManifestCopyHasNoScripts(t *testing.T) {
	dir, err := manifestCopy(tree(t, map[string]string{"package.json": withDep, "yarn.lock": "x", "src/a.js": "secret"}))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	buf, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	if strings.Contains(string(buf), "evil") || !strings.Contains(string(buf), `"a"`) {
		t.Errorf("package.json: %s", buf)
	}
	if _, err := os.Stat(filepath.Join(dir, "yarn.lock")); err != nil {
		t.Error("lockfile missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "src")); err == nil {
		t.Error("nothing but the dependency files")
	}
}
