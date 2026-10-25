package download

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/client"
)

// blockingFetch stands in for File: it reports nothing and returns only when
// its task is paused or cancelled.
func blockingFetch(ctx context.Context, _ *client.Client, _, _ string, _ Options) error {
	<-ctx.Done()
	return ctx.Err()
}

func statusOf(m *Manager, id string) Status {
	for _, s := range m.List() {
		if s.ID == id {
			return s.Status
		}
	}
	return ""
}

// A queued file has not been probed yet; the listed size is what lets a
// folder show its whole size before most of its files have started.
func TestEnqueueSeedsTotalFromListedSize(t *testing.T) {
	m := newTestManager(t)
	m.fetch = blockingFetch
	task, err := m.EnqueueEx(EnqueueOpts{Name: "a.bin", URL: "u", Dest: filepath.Join(t.TempDir(), "a.bin"), Size: 1234})
	if err != nil {
		t.Fatal(err)
	}
	if s := m.List()[0]; s.Total != 1234 {
		t.Errorf("total = %d, want the listed 1234", s.Total)
	}
	m.cancelMany(map[string]bool{task.ID: true}, true)
}

// Pausing, resuming or cancelling a folder acts on its files and on nothing
// else. The waiting forms of pause and cancel are used so that no settling
// goroutine outlives the test; PauseIDs and CancelIDs only add the ID set.
func TestIDOpsTouchOnlyTheListedTasks(t *testing.T) {
	m := newTestManager(t)
	m.fetch = blockingFetch
	dir := t.TempDir()
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		task, err := m.EnqueueEx(EnqueueOpts{Name: name, URL: "u", Dest: filepath.Join(dir, name)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	folder, other := ids[:2], ids[2]

	if n := m.pauseMany(idSet(folder), true); n != 2 {
		t.Fatalf("paused %d, want 2", n)
	}
	for _, id := range folder {
		if st := statusOf(m, id); st != StatusPaused {
			t.Errorf("%s after pause = %s", id, st)
		}
	}
	if st := statusOf(m, other); st == StatusPaused || st == StatusPausing {
		t.Errorf("task outside the folder was paused too (%s)", st)
	}

	if n := m.ResumeIDs(folder); n != 2 {
		t.Fatalf("ResumeIDs = %d, want 2", n)
	}
	for _, id := range folder {
		if st := statusOf(m, id); st != StatusQueued && st != StatusRunning {
			t.Errorf("%s after resume = %s", id, st)
		}
	}

	if n := m.cancelMany(idSet(folder), true); n != 2 {
		t.Fatalf("cancelled %d, want 2", n)
	}
	if st := statusOf(m, other); st == StatusCancel {
		t.Error("task outside the folder was cancelled too")
	}
	m.cancelMany(nil, true)
}

func TestIDOpsIgnoreAnEmptyList(t *testing.T) {
	m := newTestManager(t)
	if m.PauseIDs(nil) != 0 || m.CancelIDs(nil) != 0 || m.ResumeIDs(nil) != 0 {
		t.Error("an empty ID list touched something")
	}
}
