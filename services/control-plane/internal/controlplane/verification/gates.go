package verification

import (
	"strings"
	"time"
)

type Command struct {
	Bin  string
	Args []string
}

type ProjectContext struct {
	HasGoMod       bool
	HasPackageJSON bool
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
				script := `
ALLOW="$@"
changed=$( { git diff --name-only HEAD; git ls-files --others --exclude-standard; } | sort -u)
viol=""
for f in $changed; do
  ok=0
  for pat in $ALLOW; do
    case "$pat" in
      *"/**") pfx="${pat%/\*\*}"; case "$f" in "$pfx"/*) ok=1; break;; esac ;;
      *)      [ "$f" = "$pat" ] && { ok=1; break; } ;;
    esac
  done
  [ "$ok" = "0" ] && viol="$viol $f"
done
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
				if p.HasGoMod {
					return Command{Bin: "go", Args: []string{"build", "./..."}}, true
				}
				if p.HasPackageJSON {
					script := `
files=$( { git diff --name-only HEAD; git ls-files --others --exclude-standard; } | grep -E '\.(js|mjs|cjs)$' || true)
[ -z "$files" ] && { echo "no JS files changed"; exit 0; }
fail=0
for f in $files; do node --check "$f" || fail=1; done
exit $fail
`
					return Command{Bin: "sh", Args: []string{"-c", script}}, true
				}
				return Command{}, false
			},
			Timeout:  120 * time.Second,
			Truncate: 8_000,
		},
		{
			Name:        "test",
			Description: "Run the project test suite",
			Command: func(p ProjectContext) (Command, bool) {
				if p.HasGoMod {
					return Command{Bin: "go", Args: []string{"test", "./..."}}, true
				}
				if p.HasPackageJSON {
					return Command{Bin: "npm", Args: []string{"test"}}, true
				}
				return Command{}, false
			},
			Timeout:  180 * time.Second,
			Truncate: 8_000,
		},
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}