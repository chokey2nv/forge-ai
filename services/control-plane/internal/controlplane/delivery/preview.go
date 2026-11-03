package delivery

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// Delivery modes, decided by whether the repository has a remote.
const (
	ModePullRequest = "pull_request" // push the parent branch, open a PR with gh
	ModeLocalMerge  = "local_merge"  // merge the parent branch into the target locally
	ModeUnavailable = "unavailable"  // the task has no parent branch (created before Phase 11)
)

// Remote is the repository's delivery remote: origin, else the first one.
type Remote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Host string `json:"host,omitempty"` // empty for a local-path remote
	Repo string `json:"repo,omitempty"` // owner/name when the URL has one
}

// Preview is what approving a task will do, computed live from its
// repository. Delivery uses the same computation, so the console's hint and
// the action taken cannot disagree.
type Preview struct {
	Mode         string   `json:"mode"`
	Remote       *Remote  `json:"remote"`
	TargetBranch string   `json:"targetBranch,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	GHReady      bool     `json:"ghReady"`
	GHAccount    string   `json:"ghAccount,omitempty"`
	Action       string   `json:"action"`
	Warnings     []string `json:"warnings"`
	// DeliversOnApproval: approval performs the action (false only for
	// tasks that cannot be delivered).
	DeliversOnApproval bool `json:"deliversOnApproval"`
}

// Inspector computes previews. It caches gh's login status per host, since
// the console asks every few seconds and `gh auth status` may hit the network.
type Inspector struct {
	GH       string // gh binary: a name on PATH or a path
	CacheFor time.Duration

	mu    sync.Mutex
	cache map[string]ghStatus
}

type ghStatus struct {
	found   bool
	ready   bool
	account string
	at      time.Time
}

func NewInspector(gh string) *Inspector {
	if gh == "" {
		gh = "gh"
	}
	return &Inspector{GH: gh, CacheFor: time.Minute, cache: map[string]ghStatus{}}
}

func (in *Inspector) Preview(ctx context.Context, task *domain.Task) Preview {
	p := Preview{Warnings: []string{}, TargetBranch: task.TargetBranch, Branch: task.IntegrationBranch}
	if task.TargetBranch == "" || task.IntegrationBranch == "" {
		p.Mode = ModeUnavailable
		p.Action = "Approval records the decision only: this task was created before delivery existed and has no parent branch."
		return p
	}

	p.DeliversOnApproval = true
	remote, err := FindRemote(ctx, task.RepoPath)
	if err != nil {
		p.Mode = ModeUnavailable
		p.DeliversOnApproval = false
		p.Action = "Approval cannot deliver: " + err.Error()
		return p
	}
	if remote == nil {
		p.Mode = ModeLocalMerge
		p.Action = fmt.Sprintf("Approve → merge %s into %s locally (the repository has no remote).", p.Branch, p.TargetBranch)
		return p
	}

	p.Mode = ModePullRequest
	p.Remote = remote
	where := remote.URL
	if remote.Host != "" && remote.Repo != "" {
		where = remote.Host + "/" + remote.Repo
	}
	gh := in.ghStatus(ctx, remote.Host)
	p.GHReady, p.GHAccount = gh.ready, gh.account
	as := ""
	if gh.account != "" {
		as = " as " + gh.account
	}
	p.Action = fmt.Sprintf("Approve → push %s to %s and open a pull request into %s on %s%s.", p.Branch, remote.Name, p.TargetBranch, where, as)

	switch {
	case !gh.found:
		p.Warnings = append(p.Warnings, "gh is not installed on the control plane's machine, so approval cannot open the pull request.")
	case remote.Host == "":
		p.Warnings = append(p.Warnings, fmt.Sprintf("Remote %s is a local path; gh cannot open a pull request there.", remote.Name))
	case !gh.ready:
		p.Warnings = append(p.Warnings, fmt.Sprintf("gh is not logged in to %s (run `gh auth login`), so approval cannot open the pull request.", remote.Host))
	}
	return p
}

// FindRemote returns origin, else the first remote, else nil.
func FindRemote(ctx context.Context, repoPath string) (*Remote, error) {
	out, err := git(ctx, repoPath, "remote")
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	names := strings.Fields(out)
	if len(names) == 0 {
		return nil, nil
	}
	name := names[0]
	for _, n := range names {
		if n == "origin" {
			name = n
		}
	}
	// The configured URL, not get-url's insteadOf-rewritten one: it names
	// the forge gh talks to even when git traffic is redirected (a mirror,
	// or the test harness's local bare repository).
	url, err := git(ctx, repoPath, "config", "--get", "remote."+name+".url")
	if err != nil {
		return nil, fmt.Errorf("remote %s: %w", name, err)
	}
	host, repo := ParseRemoteURL(url)
	return &Remote{Name: name, URL: url, Host: host, Repo: repo}, nil
}

var (
	scpLike = regexp.MustCompile(`^[\w.-]+@([\w.-]+):(.+)$`)                                  // git@github.com:owner/repo.git
	urlLike = regexp.MustCompile(`^(?:https?|ssh|git)://(?:[^@/]+@)?([^/:]+)(?::\d+)?/(.+)$`) // https://github.com/owner/repo
)

// ParseRemoteURL extracts the host and owner/name from a remote URL. A local
// path (or file://) has no host.
func ParseRemoteURL(url string) (host, repo string) {
	var path string
	switch {
	case strings.HasPrefix(url, "file://"):
		return "", ""
	case urlLike.MatchString(url):
		m := urlLike.FindStringSubmatch(url)
		host, path = m[1], m[2]
	case scpLike.MatchString(url):
		m := scpLike.FindStringSubmatch(url)
		host, path = m[1], m[2]
	default:
		return "", ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return host, path
}

var loggedInAccount = regexp.MustCompile(`Logged in to \S+ (?:account|as) (\S+)`)

func (in *Inspector) ghStatus(ctx context.Context, host string) ghStatus {
	in.mu.Lock()
	if s, ok := in.cache[host]; ok && time.Since(s.at) < in.CacheFor {
		in.mu.Unlock()
		return s
	}
	in.mu.Unlock()

	s := ghStatus{at: time.Now()}
	if _, err := exec.LookPath(in.GH); err == nil {
		s.found = true
		if host != "" {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			out, err := exec.CommandContext(cctx, in.GH, "auth", "status", "--hostname", host).CombinedOutput()
			cancel()
			s.ready = err == nil
			if m := loggedInAccount.FindSubmatch(out); m != nil && s.ready {
				s.account = string(m[1])
			}
		}
	}

	in.mu.Lock()
	in.cache[host] = s
	in.mu.Unlock()
	return s
}
