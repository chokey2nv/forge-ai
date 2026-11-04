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

// installDeps runs two containers: packages fetched with the network and no
// scripts, then the scripts with no network.
func TestInstallScriptsRunWithoutNetwork(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "docker.log")
	script := "#!/bin/sh\nprintf '%s\\036' \"$*\" >>" + log + "\n" // one record per call
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := installDeps(context.Background(), "/tmp/forge-child-abc", "img", time.Minute); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSuffix(string(out), "\036"), "\036")
	if len(calls) != 2 {
		t.Fatalf("want 2 docker runs, got %d:\n%s", len(calls), out)
	}
	fetch, scripts := calls[0], calls[1]
	for _, c := range calls {
		for _, want := range []string{"--cap-drop=ALL", "--read-only", "--security-opt=no-new-privileges", "--user ", "-v /tmp/forge-child-abc:/workspace:rw"} {
			if !strings.Contains(c, want) {
				t.Errorf("%q lacks %q", c, want)
			}
		}
	}
	if !strings.Contains(fetch, "--network=bridge") || !strings.Contains(fetch, "npm install --no-audit --no-fund --ignore-scripts") || strings.Contains(fetch, "npm run") || strings.Contains(fetch, "rebuild") {
		t.Errorf("fetch step: %s", fetch)
	}
	if !strings.Contains(scripts, "--network=none") || !strings.Contains(scripts, "npm rebuild") || !strings.Contains(scripts, "postinstall") {
		t.Errorf("scripts step: %s", scripts)
	}
}
