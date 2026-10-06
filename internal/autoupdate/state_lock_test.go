package autoupdate

// Authored for story 056, sub-task 2.2 (S056-R5.1, S056-R5.2, S056-R5.6).
//
// The holder child takes the lock with the raw mechanism the story names —
// a 0644 file, flock(LOCK_EX), pid=<pid> — rather than through filelock, so the
// test does not agree with the code under test merely by sharing it.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

func s056Bounds(t *testing.T, wait, poll time.Duration) {
	t.Helper()
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = wait, poll
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })
}

// TestStateSave_InProcessSavesSerialize: R5.6. Four Cache and four PendingList
// instances over ONE config dir save at once. They share one lock file, so a
// save that polled it would sleep a full Poll; Poll is set to 30s, so only an
// in-process queue in front of the file lock finishes inside the bound. Every
// key must also survive, which is R5.4 under real concurrency.
func TestStateSave_InProcessSavesSerialize(t *testing.T) {
	s056Bounds(t, 2*time.Minute, 30*time.Second)
	dir := t.TempDir()

	var caches []*fetch.Cache
	var pendings []*PendingList
	for range 4 {
		c, err := fetch.NewCache(dir)
		if err != nil {
			t.Fatal(err)
		}
		p, err := NewPendingList(dir)
		if err != nil {
			t.Fatal(err)
		}
		caches, pendings = append(caches, c), append(pendings, p)
	}

	errs := make(chan error, 8)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 4 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- caches[i].Set(fmt.Sprintf("x/c%d", i), "1", "u")
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- pendings[i].Add(PendingUpdate{Package: fmt.Sprintf("x/p%d", i), CurrentVersion: "1", NewVersion: "2", Status: StatusPending})
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	close(start)

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("eight concurrent saves did not finish in 20s with Poll=30s: in-process saves are polling the file lock instead of queueing")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent save failed: %v", err)
		}
	}

	c, _ := fetch.NewCache(dir)
	p, _ := NewPendingList(dir)
	for i := range 4 {
		if _, ok := c.GetEntry(fmt.Sprintf("x/c%d", i)); !ok {
			t.Errorf("cache.json lost x/c%d", i)
		}
		if !p.Has(fmt.Sprintf("x/p%d", i)) {
			t.Errorf("pending.json lost x/p%d", i)
		}
	}
}
