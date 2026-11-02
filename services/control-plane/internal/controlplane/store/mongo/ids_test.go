package mongo_store

import (
	"sync"
	"testing"
)

// Parallel subtasks record events from several goroutines at once; their
// IDs must not collide.
func TestDocumentIDsAreUniqueUnderConcurrency(t *testing.T) {
	const n = 2000
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); ids <- uuidShort() }()
	}
	wg.Wait()
	close(ids)
	seen := make(map[string]bool, n)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate ID %s", id)
		}
		seen[id] = true
	}
}
