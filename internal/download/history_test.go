package download

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func historyItem(id, name string, st Status, created time.Time) PersistTask {
	name = filepath.FromSlash(name)
	return PersistTask{ID: id, Name: name, Dest: filepath.Join("/dl", name), Status: st, CreatedAt: created}
}

func countByGroup(items []PersistTask) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[historyGroup(it)]++
	}
	return out
}

func TestHistoryGroupIsTheTopFolder(t *testing.T) {
	a := historyGroup(historyItem("1", "专辑/CD1/01.flac", StatusDone, time.Time{}))
	b := historyGroup(historyItem("2", "专辑/封面.jpg", StatusDone, time.Time{}))
	c := historyGroup(historyItem("3", "单曲.mp3", StatusDone, time.Time{}))
	if a != b || a != "dir:"+filepath.Join("/dl", "专辑") {
		t.Errorf("files of one folder in groups %q and %q", a, b)
	}
	if c != "task:3" {
		t.Errorf("single file group = %q", c)
	}
}

// A folder counts once against the history cap and is kept or dropped
// whole, however many files it holds.
func TestHistoryKeepsFoldersWhole(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	var finished []PersistTask
	// the oldest download: a folder of 300 files
	for i := 0; i < 300; i++ {
		finished = append(finished, historyItem(fmt.Sprintf("f%d", i), fmt.Sprintf("old/%d.bin", i), StatusDone, base))
	}
	// newer: a folder of 350 files, then singles that push the total past the cap
	for i := 0; i < 350; i++ {
		finished = append(finished, historyItem(fmt.Sprintf("a%d", i), fmt.Sprintf("album/%d.flac", i), StatusDone, base.Add(time.Minute)))
	}
	for i := 0; i < maxFinishedKeep-1; i++ {
		finished = append(finished, historyItem(fmt.Sprintf("s%d", i), fmt.Sprintf("single-%d.bin", i), StatusDone, base.Add(time.Duration(i+2)*time.Minute)))
	}
	// 201 downloads (849 files): only the oldest download goes, and whole

	got := countByGroup(trimHistory(nil, finished))
	if len(got) != maxFinishedKeep {
		t.Errorf("kept %d downloads, want %d", len(got), maxFinishedKeep)
	}
	if n := got["dir:"+filepath.Join("/dl", "old")]; n != 0 {
		t.Errorf("the oldest folder kept %d of its 300 files, want none", n)
	}
	if n := got["dir:"+filepath.Join("/dl", "album")]; n != 350 {
		t.Errorf("album kept with %d of its 350 files", n)
	}
	if got["task:s0"] != 1 {
		t.Error("the oldest single was dropped although it is among the newest 200 downloads")
	}
}

// A folder with a file still paused keeps all its finished files, however
// old: dropping them would leave the folder showing one file.
func TestHistoryKeepsFoldersWithActiveFiles(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)
	active := []PersistTask{historyItem("p", "old/paused.bin", StatusPaused, old)}
	var finished []PersistTask
	for i := 0; i < 50; i++ {
		finished = append(finished, historyItem(fmt.Sprintf("f%d", i), fmt.Sprintf("old/%d.bin", i), StatusDone, old))
	}
	for i := 0; i < maxFinishedKeep+10; i++ {
		finished = append(finished, historyItem(fmt.Sprintf("s%d", i), fmt.Sprintf("single-%d.bin", i), StatusDone, time.Now()))
	}
	got := countByGroup(trimHistory(active, finished))
	if n := got["dir:"+filepath.Join("/dl", "old")]; n != 50 {
		t.Errorf("folder with an active file kept %d of its 50 finished files", n)
	}
}

func TestHistoryUnderTheCapIsUntouched(t *testing.T) {
	finished := []PersistTask{
		historyItem("1", "a.bin", StatusDone, time.Now()),
		historyItem("2", "d/b.bin", StatusCancel, time.Now()),
	}
	if got := trimHistory(nil, finished); len(got) != 2 {
		t.Errorf("trimmed %d of 2", 2-len(got))
	}
}
