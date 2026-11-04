package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOutsideScope(t *testing.T) {
	patterns := []string{"src/price.js", "tests/*", "docs/"}
	if got := outsideScope([]string{"src/price.js", "tests/a.test.js", "docs/deep/report.md"}, patterns); len(got) != 0 {
		t.Errorf("all covered: %v", got)
	}
	got := outsideScope([]string{"src/price.js", "src/other.js", "tests/deep/a.js"}, patterns)
	if len(got) != 2 || !strings.Contains(got[0], "src/other.js") || !strings.Contains(got[1], "tests/deep/a.js") {
		t.Errorf("outside: %v", got)
	}
	if got := outsideScope(nil, patterns); len(got) != 1 || got[0] != "changed no files" {
		t.Errorf("no change is never a pass: %v", got)
	}
	if got := outsideScope([]string{"anything.js"}, nil); len(got) != 0 {
		t.Errorf("no patterns, no limit: %v", got)
	}
}

func TestFailureLine(t *testing.T) {
	if got := failureLine("node:assert:1\n  throw err\nAssertionError [ERR_ASSERTION]: 400 == 4\n  at x\n}\n\nNode.js v22"); got != "AssertionError [ERR_ASSERTION]: 400 == 4" {
		t.Errorf("the error: %q", got)
	}
	if got := failureLine("one\nlast line\n"); got != "last line" {
		t.Errorf("no error line: %q", got)
	}
}

func TestSelectCases(t *testing.T) {
	all := []string{"a/0001-one.json", "a/0002-two.json"}
	if got := selectCases(all, nil); !slices.Equal(got, all) {
		t.Errorf("no filter: %v", got)
	}
	if got := selectCases(all, []string{"0002"}); !slices.Equal(got, []string{"a/0002-two.json"}) {
		t.Errorf("filter: %v", got)
	}
}

// Every case in the suite loads, has a fixture and something to score.
func TestSuite(t *testing.T) {
	root := "../../../.."
	paths, _ := filepath.Glob(filepath.Join(root, "tests/evaluations/*.json"))
	if len(paths) < 4 {
		t.Fatalf("only %d cases", len(paths))
	}
	for _, p := range paths {
		c, err := loadCase(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, c.FixtureRepo, "package.json")); err != nil {
			t.Errorf("%s: fixture: %v", c.ID, err)
		}
		if len(c.Checks.Commands) == 0 || len(c.Checks.OnlyChanged) == 0 {
			t.Errorf("%s: a case needs commands and onlyChanged", c.ID)
		}
	}
}
