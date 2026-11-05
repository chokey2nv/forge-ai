package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A project's installed packages live in a Docker volume named for what
// determines them, so they're installed once and every workspace with the same
// dependency files mounts them. Workspaces see them read-only: an agent can't
// change what other tasks run. On Docker Desktop a volume also reads far faster
// than a folder shared from the host.

const (
	// depsLabel marks the volumes the reaper may remove.
	depsLabel = "forge.deps=1"
	// depsMarker is written last, so an install that failed part-way is never used.
	depsMarker = ".forge-complete"
	// pnpmStoreVolume keeps pnpm's downloads for the next install. On a host
	// folder, installs failed with "too many open files".
	pnpmStoreVolume = "forge-pnpm-store"
	// depsMaxUnused is how long a volume no workspace asks for is kept.
	depsMaxUnused = 14 * 24 * time.Hour
)

// Files that decide what an install produces, besides package.json.
var depsFiles = []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "pnpm-workspace.yaml", ".npmrc", ".yarnrc", ".yarnrc.yml"}

// Folders tools write caches to inside node_modules: writable, for one container.
var depsCacheDirs = []string{".vite", ".cache"}

var (
	depsLocks sync.Map // volume name -> *sync.Mutex: one install at a time
	depsReady sync.Map // volume name -> true: seen complete by this process
)

// nodeManifest is what's read from a package.json.
type nodeManifest struct {
	Workspaces           json.RawMessage   `json:"workspaces"`
	Scripts              map[string]string `json:"scripts"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

func readManifest(dir string) (nodeManifest, bool) {
	var m nodeManifest
	buf, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil || json.Unmarshal(buf, &m) != nil {
		return m, false
	}
	return m, true
}

func (m nodeManifest) hasDependencies() bool {
	return len(m.Dependencies)+len(m.DevDependencies)+len(m.OptionalDependencies) > 0
}

var localDependency = regexp.MustCompile(`^(file|link|portal|workspace):|^\.{0,2}/`)
var pnpmPackagesKey = regexp.MustCompile(`(?m)^packages\s*:`)

// shareable: its packages depend only on its dependency files. A monorepo's
// node_modules folders are spread through it, and a local dependency needs the
// repository's files, so those install in each workspace as before.
func shareable(dir string, p Project, m nodeManifest) bool {
	if p.Go || len(m.Workspaces) > 0 {
		return false
	}
	if buf, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml")); err == nil && pnpmPackagesKey.Match(buf) {
		return false
	}
	for _, deps := range []map[string]string{m.Dependencies, m.DevDependencies, m.OptionalDependencies} {
		for _, v := range deps {
			if localDependency.MatchString(v) {
				return false
			}
		}
	}
	return true
}

// depsVolumeName covers everything that decides the install, and the image,
// since packages can build native code for it.
func depsVolumeName(dir string, p Project, imageID string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", p.Dir, p.PM, imageID)
	for _, name := range append([]string{"package.json"}, depsFiles...) {
		buf, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(buf))
		h.Write(buf)
	}
	return "forge-deps-" + hex.EncodeToString(h.Sum(nil))[:20], nil
}

// mountPath is where a project's node_modules is in a container.
func mountPath(p Project) string {
	return path.Join("/workspace", filepath.ToSlash(p.Dir), "node_modules")
}

// depsMountArgs are what a container needs to use a volume read-only.
func depsMountArgs(volume string, p Project) []string {
	target := mountPath(p)
	uid, gid := containerUser()
	args := []string{"-v", volume + ":" + target + ":ro"}
	for _, cache := range depsCacheDirs {
		args = append(args, "--tmpfs", fmt.Sprintf("%s/%s:rw,size=256m,uid=%d,gid=%d", target, cache, uid, gid))
	}
	return args
}

func imageID(ctx context.Context, image string) string {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return image // still separates images by name
	}
	return strings.TrimSpace(string(out))
}

// ensureDepsVolume returns once the volume holds a finished install.
func ensureDepsVolume(ctx context.Context, name, worktreeDir string, p Project, image string) error {
	lock, _ := depsLocks.LoadOrStore(name, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	touchDepsVolume(name)

	exists := exec.CommandContext(ctx, "docker", "volume", "inspect", name).Run() == nil
	if exists {
		if _, ok := depsReady.Load(name); ok {
			return nil
		}
		check := exec.CommandContext(ctx, "docker", "run", "--rm", "--network=none", "-v", name+":/v:ro", image, "test", "-f", "/v/"+depsMarker)
		if check.Run() == nil {
			depsReady.Store(name, true)
			return nil
		}
		// A build that didn't finish. In use, this fails, and the mount can't be
		// trusted anyway: the install below finds out.
		_ = exec.CommandContext(ctx, "docker", "volume", "rm", name).Run()
	}
	depsReady.Delete(name)
	if err := buildDepsVolume(ctx, name, worktreeDir, p, image); err != nil {
		_ = exec.CommandContext(context.WithoutCancel(ctx), "docker", "volume", "rm", name).Run()
		return err
	}
	depsReady.Store(name, true)
	return nil
}

// ownedVolume creates a volume the containers' user can write to.
func ownedVolume(ctx context.Context, name, image string, labels ...string) error {
	args := []string{"volume", "create"}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	if out, err := exec.CommandContext(ctx, "docker", append(args, name)...).CombinedOutput(); err != nil {
		return fmt.Errorf("create volume %s: %s: %w", name, out, err)
	}
	uid, gid := containerUser()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network=none", "--user", "0", "-v", name+":/v", image,
		"chown", fmt.Sprintf("%d:%d", uid, gid), "/v").CombinedOutput()
	if err != nil {
		return fmt.Errorf("prepare volume %s: %s: %w", name, out, err)
	}
	return nil
}

// buildDepsVolume installs from the dependency files alone, with the project's
// own scripts removed: nothing an agent wrote elsewhere in the repository can
// reach a volume other tasks will use. Packages are fetched with the network
// and none of their scripts run; those then run with no network (Phase 13.5).
func buildDepsVolume(ctx context.Context, name, worktreeDir string, p Project, image string) error {
	if err := ownedVolume(ctx, name, image, depsLabel); err != nil {
		return err
	}
	manifests, err := manifestCopy(worktreeDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(manifests)

	workdir := path.Join("/workspace", filepath.ToSlash(p.Dir))
	mounts := []string{"--workdir", workdir, "-v", manifests + ":" + workdir + ":rw", "-v", name + ":" + mountPath(p)}
	step := func(network, script string, extra ...string) error {
		args := append([]string{"run", "--rm", "--label", LabelRun + "=setup-" + name, "--label", instanceLabel(), "--network=" + network}, mounts...)
		args = append(args, extra...)
		args = append(args, sandboxArgs()...)
		args = append(args, image, "sh", "-c", script)
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("install (%s network): %s: %w", network, tail(out, 1500), err)
		}
		return nil
	}

	var store []string
	if p.PM == PNPM {
		if store, err = pnpmStoreArgs(ctx, image); err != nil {
			return err
		}
	}
	if err := step("bridge", fetchCommand(p), store...); err != nil {
		return err
	}
	finish := fmt.Sprintf("%s && mkdir -p %s && : > node_modules/%s", rebuildCommand(p),
		"node_modules/"+strings.Join(depsCacheDirs, " node_modules/"), depsMarker)
	return step("none", finish)
}

// pnpmStoreArgs mounts pnpm's store, for a fetch only: it runs no scripts.
func pnpmStoreArgs(ctx context.Context, image string) ([]string, error) {
	if exec.CommandContext(ctx, "docker", "volume", "inspect", pnpmStoreVolume).Run() != nil {
		if err := ownedVolume(ctx, pnpmStoreVolume, image); err != nil {
			return nil, err
		}
	}
	return []string{"-v", pnpmStoreVolume + ":" + pnpmStore, "--ulimit", "nofile=65536:65536"}, nil
}

// manifestCopy is a temporary folder with the dependency files, and a
// package.json without its scripts.
func manifestCopy(worktreeDir string) (string, error) {
	dir, err := mktempDir("forge-deps-")
	if err != nil {
		return "", err
	}
	buf, err := os.ReadFile(filepath.Join(worktreeDir, "package.json"))
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	var pkg map[string]any
	if err := json.Unmarshal(buf, &pkg); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("package.json: %w", err)
	}
	delete(pkg, "scripts")
	out, _ := json.MarshalIndent(pkg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "package.json"), out, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	for _, name := range depsFiles {
		content, err := os.ReadFile(filepath.Join(worktreeDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, name), content, 0o644)
		}
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func tail(out []byte, n int) string {
	s := strings.TrimSpace(string(out))
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// lifecycleScripts: the project's own install scripts it declares.
func lifecycleScripts(m nodeManifest) bool {
	for _, s := range []string{"preinstall", "install", "postinstall", "prepare"} {
		if m.Scripts[s] != "" {
			return true
		}
	}
	return false
}

// usedDir records when each volume was last asked for: Docker doesn't.
func usedDir() string {
	base := os.TempDir()
	if cache, err := os.UserCacheDir(); err == nil {
		base = cache
	}
	return filepath.Join(base, "forge", "deps-used")
}

func touchDepsVolume(name string) {
	dir := usedDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	file := filepath.Join(dir, name)
	now := time.Now()
	if os.Chtimes(file, now, now) != nil {
		_ = os.WriteFile(file, nil, 0o644)
	}
}

// RemoveUnusedDepsVolumes removes volumes no workspace has asked for in a while.
// Docker refuses to remove one a container is using.
func RemoveUnusedDepsVolumes(ctx context.Context) {
	out, err := exec.CommandContext(ctx, "docker", "volume", "ls", "-q", "--filter", "label="+depsLabel).Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		info, err := os.Stat(filepath.Join(usedDir(), name))
		if err != nil {
			touchDepsVolume(name) // the clock starts now
			continue
		}
		if time.Since(info.ModTime()) > depsMaxUnused && exec.CommandContext(ctx, "docker", "volume", "rm", name).Run() == nil {
			depsReady.Delete(name)
			_ = os.Remove(filepath.Join(usedDir(), name))
			log.Printf("reaper: removed unused package volume %s", name)
		}
	}
}
