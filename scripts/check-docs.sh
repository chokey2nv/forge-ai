#!/usr/bin/env bash
# Fails if the docs have drifted from the repository: a code folder with no
# README (its own or its parent's), or a doc naming a file or make target that
# doesn't exist. Run with: make docs-check
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2
failed=0
fail() { printf '  %s\n' "$*"; failed=1; }

# Tracked and new files; a deleted file still in the index doesn't count.
files="$(git ls-files --cached --others --exclude-standard | while IFS= read -r f; do [ -e "$f" ] && printf '%s\n' "$f"; done)"
docs="$(printf '%s\n' "$files" | grep -E '(^|/)(README|CLAUDE|ARCHITECTURE)\.md$')"
targets="$(grep -E '^[a-zA-Z0-9_-]+:' Makefile | cut -d: -f1)"
has_doc() { printf '%s\n' "$docs" | grep -qxF "$1"; }

echo "Code folders without a README:"
printf '%s\n' "$files" | grep -E '\.(go|ts|tsx)$' | grep -vE '(_test\.go|\.test\.ts|\.d\.ts)$' | grep -v '^tests/evaluations/fixtures/' |
  xargs -n1 dirname | sort -u > "${TMPDIR:-/tmp}/forge-docs-dirs.$$"
while IFS= read -r dir; do
  has_doc "$dir/README.md" || has_doc "$(dirname "$dir")/README.md" || has_doc "$(dirname "$(dirname "$dir")")/README.md" && continue
  fail "$dir"
done < "${TMPDIR:-/tmp}/forge-docs-dirs.$$"
rm -f "${TMPDIR:-/tmp}/forge-docs-dirs.$$"

echo "Docs naming something that doesn't exist:"
# A bare file name may live anywhere. A path resolves against the doc's folder,
# the repo root, or the control plane's packages, which docs name without the
# long prefix.
PKG=services/control-plane/internal/controlplane
exists() { # doc name
  case "$2" in
    */*) [ -e "$(dirname "$1")/$2" ] || [ -e "$2" ] || [ -e "$PKG/$2" ] ;;
    *) printf '%s\n' "$files" | grep -qE "(^|/)$(printf '%s' "$2" | sed 's/\./\\./g')$" ;;
  esac
}
while IFS= read -r doc; do
  # `path.ext` or `dir/` in code spans; placeholders like <name> are skipped.
  names="$(grep -oE '`[A-Za-z0-9_./-]+`' "$doc" | tr -d '`' | grep -E '(^[A-Za-z0-9_].*\.(go|ts|tsx|sh|md|css|json|yaml|command)$|^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*/$)')"
  links="$(grep -oE '\]\([^)#: ]+' "$doc" | sed 's/^](//')"
  for n in $names; do
    exists "$doc" "$n" || fail "$doc: $n"
  done
  for l in $links; do
    [ -e "$(dirname "$doc")/$l" ] || fail "$doc: link $l"
  done
  for t in $(grep -oE '`make [a-zA-Z0-9_-]+' "$doc" | awk '{print $2}'); do
    printf '%s\n' "$targets" | grep -qxF "$t" || fail "$doc: make $t"
  done
done <<DOCS
$docs
DOCS

if [ "$failed" = 0 ]; then echo "Docs check passed."; else echo "Docs check failed."; exit 1; fi
