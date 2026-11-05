package verification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/workspace"
)

type Runner struct {
	Gates []Gate
}

func NewRunner() *Runner {
	return &Runner{Gates: DefaultPolicy()}
}

func (r *Runner) Run(ctx context.Context, containerName, worktreePath string, diffScope []string) *domain.VerificationRun {
	start := time.Now()
	vr := &domain.VerificationRun{
		StartedAt: start,
	}

	pctx := ProjectContext{Projects: workspace.Projects(worktreePath), DiffScopeAllow: diffScope}
	vr.WorkspaceDiff = getDiff(ctx, containerName)

	for _, g := range r.Gates {
		cmd, shouldRun := g.Command(pctx)
		if !shouldRun {
			vr.Gates = append(vr.Gates, domain.GateResult{
				Name: g.Name, Passed: true, Skipped: true,
				SkipReason: "not applicable to this project",
			})
			continue
		}

		result := workspace.RunInContainer(ctx, containerName,
			append([]string{cmd.Bin}, cmd.Args...), g.Timeout)

		out := result.Stdout
		if result.Stderr != "" {
			out = out + "\n" + result.Stderr
		}
		out = truncateOutput(out, g.Truncate)

		vr.Gates = append(vr.Gates, domain.GateResult{
			Name:       g.Name,
			Passed:     result.ExitCode == 0,
			ExitCode:   result.ExitCode,
			Output:     out,
			DurationMS: int(result.Duration / time.Millisecond),
		})
	}

	vr.FinishedAt = time.Now()
	vr.DurationMS = int(vr.FinishedAt.Sub(start) / time.Millisecond)
	vr.Passed = true
	for _, g := range vr.Gates {
		if !g.Passed && !g.Skipped {
			vr.Passed = false
			break
		}
	}
	return vr
}

// truncateOutput keeps mostly the end: that's where a test run lists its
// failures and totals.
func truncateOutput(out string, limit int) string {
	if limit <= 0 || len(out) <= limit {
		return out
	}
	head := limit / 4
	return out[:head] + "\n...[truncated]...\n" + out[len(out)-(limit-head):]
}

func getDiff(ctx context.Context, containerName string) string {
	workspace.RunInContainer(ctx, containerName, []string{"git", "add", "-A"}, 10*time.Second)
	r := workspace.RunInContainer(ctx, containerName, []string{"git", "diff", "--cached", "HEAD"}, 10*time.Second)
	workspace.RunInContainer(ctx, containerName, []string{"git", "reset"}, 5*time.Second)
	return r.Stdout
}

func (r *Runner) SummaryText(vr *domain.VerificationRun) string {
	var b strings.Builder
	for _, g := range vr.Gates {
		switch {
		case g.Skipped:
			fmt.Fprintf(&b, "- %s: skipped (%s)\n", g.Name, g.SkipReason)
		case g.Passed:
			fmt.Fprintf(&b, "- %s: passed (%dms)\n", g.Name, g.DurationMS)
		default:
			fmt.Fprintf(&b, "- %s: FAILED (exit %d)\n%s\n", g.Name, g.ExitCode, g.Output)
		}
	}
	return b.String()
}
