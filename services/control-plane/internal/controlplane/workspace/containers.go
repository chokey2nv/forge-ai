package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// LabelRun marks every container this package starts (agent workspaces and
// dependency-install containers), so leaked ones can be found after a crash.
const LabelRun = "forge.run"

// Container is a container carrying LabelRun.
type Container struct {
	Name    string
	Label   string // value of LabelRun
	Created time.Time
}

// ListManagedContainers returns all containers, running or not, that carry LabelRun.
func ListManagedContainers(ctx context.Context) ([]Container, error) {
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label="+LabelRun).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	fields := strings.Fields(string(ids))
	if len(fields) == 0 {
		return nil, nil
	}

	args := append([]string{"inspect", "--format",
		`{{.Name}}|{{.Created}}|{{index .Config.Labels "` + LabelRun + `"}}`}, fields...)
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}

	var cs []Container
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, parts[1])
		if err != nil {
			continue
		}
		cs = append(cs, Container{
			Name:    strings.TrimPrefix(parts[0], "/"),
			Created: created,
			Label:   parts[2],
		})
	}
	return cs, nil
}

// RemoveContainer force-removes a container; a missing container is not an error.
func RemoveContainer(ctx context.Context, name string) {
	if name == "" {
		return
	}
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}
