package runner

import (
	"context"
	"log"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/statemachine"
	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store"
)

type Watcher struct {
	Runs       store.RunRepo
	Tasks      store.TaskRepo
	SM         *statemachine.Service
	StaleAfter time.Duration
}

func NewWatcher(runs store.RunRepo, tasks store.TaskRepo, sm *statemachine.Service, staleAfter time.Duration) *Watcher {
	return &Watcher{Runs: runs, Tasks: tasks, SM: sm, StaleAfter: staleAfter}
}

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-w.StaleAfter)
			stale, err := w.Runs.MarkStale(ctx, cutoff)
			if err != nil {
				log.Printf("watcher: MarkStale: %v", err)
				continue
			}
			for _, run := range stale {
				task, err := w.Tasks.Get(ctx, run.TaskID)
				if err != nil {
					log.Printf("watcher: get task %s: %v", run.TaskID, err)
					continue
				}
				if task.Status != domain.StatusRunning {
					continue
				}
				if err := w.SM.Transition(ctx, task, domain.StatusResumable, "no heartbeat"); err != nil {
					log.Printf("watcher: transition %s: %v", task.ID, err)
					continue
				}
				log.Printf("watcher: run %s marked RESUMABLE (task %s)", run.ID, task.ID)
			}
		}
	}
}
