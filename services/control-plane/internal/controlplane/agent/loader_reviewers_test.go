package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, dir, name, submission string, reviewers ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("---\nname: " + name + "\ndescription: test agent\nversion: 1\n")
	if submission != "" {
		b.WriteString("submission_tool: " + submission + "\n")
	}
	b.WriteString("tools:\n  - read_file\nreviewers:")
	if len(reviewers) == 0 {
		b.WriteString(" []\n")
	} else {
		b.WriteString("\n")
		for _, r := range reviewers {
			b.WriteString("  - " + r + "\n")
		}
	}
	b.WriteString("---\n\nPrompt.\n")
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderRejectsBadReviewers(t *testing.T) {
	cases := map[string]struct {
		reviewers []string
		wantErr   string
	}{
		"review agent":      {[]string{"rev"}, ""},
		"two review agents": {[]string{"rev", "rev2"}, ""},
		"unknown":           {[]string{"ghost"}, `unknown reviewer "ghost"`},
		"work agent":        {[]string{"other-worker"}, `"other-worker", which is not a review agent`},
		"self":              {[]string{"worker"}, "lists itself"},
	}
	for name, c := range cases {
		dir := t.TempDir()
		writeAgent(t, dir, "rev", "submit_review")
		writeAgent(t, dir, "rev2", "submit_review")
		writeAgent(t, dir, "other-worker", "")
		writeAgent(t, dir, "worker", "", c.reviewers...)

		res, err := LoadAgentsDir(dir, map[string]*Skill{})
		if err != nil {
			t.Fatal(err)
		}
		var msgs []string
		for _, e := range res.Errors {
			msgs = append(msgs, e.Error())
		}
		joined := strings.Join(msgs, "; ")
		if c.wantErr == "" && joined != "" {
			t.Errorf("%s: unexpected errors: %s", name, joined)
		}
		if c.wantErr != "" && !strings.Contains(joined, c.wantErr) {
			t.Errorf("%s: want error containing %q, got %q", name, c.wantErr, joined)
		}
	}
}
