package verification

import (
	"os"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

type Command struct {
	Bin  string
	Args []string
}

type ProjectContext struct {
	Projects       []workspace.Project
	DiffScopeAllow []string
}

type Gate struct {
	Name        string
	Description string
	Command     func(ProjectContext) (Command, bool) // (cmd, shouldRun)
	Timeout     time.Duration
	Truncate    int
}

func DefaultPolicy() []Gate {
	return []Gate{
		{
			Name:        "secrets",
			Description: "Scan added diff lines for secret-like patterns",
			Command: func(ProjectContext) (Command, bool) {
				script := `
PATTERN='(AKIA[0-9A-Z]{16}|-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----|sk-[A-Za-z0-9]{20,})'
HITS=$(git diff -U0 HEAD | grep -E '^\+' | grep -vE '^\+\+\+' | grep -E "$PATTERN" || true)
if [ -n "$HITS" ]; then echo "Potential secrets in added lines:"; echo "$HITS"; exit 1; fi
`
				return Command{Bin: "sh", Args: []string{"-c", script}}, true
			},
			Timeout:  30 * time.Second,
			Truncate: 4_000,
		},
		{
			Name:        "diff-scope",
			Description: "Modified files must match the allowed scope",
			Command: func(p ProjectContext) (Command, bool) {
				if len(p.DiffScopeAllow) == 0 {
					return Command{}, false
				}
				// "$@" keeps a pattern whole: expanded unquoted, the shell turns
				// dir/** into dir's top-level entries, and files below them fail.
				script := `
changed=$( { git diff --name-only HEAD; git ls-files --others --exclude-standard; } | sort -u)
viol=""
while IFS= read -r f; do
  [ -n "$f" ] || continue
  ok=0
  for pat in "$@"; do
    case "$pat" in
      *"/**") pfx="${pat%/\*\*}"; case "$f" in "$pfx"/*) ok=1; break;; esac ;;
      *)      [ "$f" = "$pat" ] && { ok=1; break; } ;;
    esac
  done
  [ "$ok" = "0" ] && viol="$viol $f"
done <<EOF
$changed
EOF
if [ -n "$viol" ]; then echo "Files outside allowed scope:$viol"; exit 1; fi
`
				args := append([]string{"-c", script, "sh"}, p.DiffScopeAllow...)
				return Command{Bin: "sh", Args: args}, true
			},
			Timeout:  30 * time.Second,
			Truncate: 4_000,
		},
		{
			Name:        "build",
			Description: "Compile or syntax-check changed files",
			Command: func(p ProjectContext) (Command, bool) {
				if len(p.Projects) == 0 {
					return Command{}, false
				}
				script := workspace.ForEachProject(p.Projects, func(pr workspace.Project) string {
					if pr.Go {
						return "go build ./..."
					}
					return nodeSyntaxCheck
				})
				return Command{Bin: "sh", Args: []string{"-c", script}}, true
			},
			Timeout:  120 * time.Second,
			Truncate: 8_000,
		},
		{
			Name:        "test",
			Description: "Run the project test suite",
			Command: func(p ProjectContext) (Command, bool) {
				var tested []workspace.Project
				for _, pr := range p.Projects {
					if pr.HasTest {
						tested = append(tested, pr)
					}
				}
				if len(tested) == 0 {
					return Command{}, false
				}
				script := workspace.ForEachProject(tested, func(pr workspace.Project) string {
					if pr.Go {
						return "go test ./..."
					}
					return pr.PM + " test"
				})
				return Command{Bin: "sh", Args: []string{"-c", script}}, true
			},
			Timeout:  testTimeout(),
			Truncate: 8_000,
		},
	}
}

// testTimeout: a real project's suite can take many minutes in a container.
func testTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("VERIFICATION_TEST_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return 15 * time.Minute
}

// nodeSyntaxCheck checks the JS changed in the project it runs in.
const nodeSyntaxCheck = `files=$( { git diff --relative --name-only HEAD; git ls-files --others --exclude-standard; } | grep -E '\.(js|mjs|cjs)$' || true)
if [ -z "$files" ]; then echo "no JS files changed"; exit 0; fi
rc=0
for f in $files; do node --check "$f" || rc=1; done
exit $rc`

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}