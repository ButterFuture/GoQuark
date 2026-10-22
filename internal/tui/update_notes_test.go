package tui

import (
	"strings"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/update"
)

func modelWithNotes(notes string) model {
	return model{
		height:        40,
		modalKind:     "confirm",
		confirmAction: "update_available",
		updateRes: &update.Result{
			Current: "1.0.3", Latest: "1.0.4", CanApply: true, Notes: notes,
		},
	}
}

func TestUpdateConfirmBodyCollapsesLongNotes(t *testing.T) {
	notes := strings.TrimSpace(strings.Repeat("a line of release notes\n", 30))
	m := modelWithNotes(notes)

	collapsed := m.updateConfirmBody()
	if !strings.Contains(collapsed, "发现新版本 1.0.4") {
		t.Error("version line missing")
	}
	if !strings.Contains(collapsed, "更新说明") {
		t.Error("notes heading missing")
	}
	if !strings.Contains(collapsed, "展开完整说明") {
		t.Error("expand button missing on long notes")
	}

	m = m.toggleUpdateNotes()
	expanded := m.updateConfirmBody()
	if !strings.Contains(expanded, "收起说明") {
		t.Error("collapse button missing once expanded")
	}
	if countLines(expanded) <= countLines(collapsed) {
		t.Errorf("expanding showed no more lines: %d then %d",
			countLines(collapsed), countLines(expanded))
	}

	m = m.toggleUpdateNotes()
	if got := m.updateConfirmBody(); countLines(got) != countLines(collapsed) {
		t.Error("collapsing again did not restore the short body")
	}
}

// Short notes need no control at all.
func TestUpdateConfirmBodyShortNotesHaveNoToggle(t *testing.T) {
	m := modelWithNotes("one line")
	body := m.updateConfirmBody()
	if !strings.Contains(body, "one line") {
		t.Error("short notes were not shown")
	}
	if strings.Contains(body, "展开完整说明") || strings.Contains(body, "收起说明") {
		t.Error("toggle offered for notes that already fit")
	}
	if before := m.notesExpanded; m.toggleUpdateNotes().notesExpanded != before {
		t.Error("toggling did something with nothing to expand")
	}
}

func TestUpdateConfirmBodyWithoutNotes(t *testing.T) {
	m := modelWithNotes("")
	body := m.updateConfirmBody()
	if strings.Contains(body, "更新说明") {
		t.Error("empty notes still rendered a heading")
	}
	if !strings.Contains(body, "点击「立即更新」即可安装。") {
		t.Error("the usual prompt went missing")
	}
}

// A short terminal must not get a dialog taller than the screen.
func TestUpdateConfirmBodyRespectsTerminalHeight(t *testing.T) {
	notes := strings.TrimSpace(strings.Repeat("line\n", 200))
	m := modelWithNotes(notes)
	m.height = 24
	m.notesExpanded = true
	body := m.updateConfirmBody()
	if countLines(body) > m.height {
		t.Errorf("expanded body is %d lines on a %d-line terminal", countLines(body), m.height)
	}
	if !strings.Contains(body, "完整说明见发布页") {
		t.Error("no hint that the notes were cut short")
	}
}

func countLines(s string) int { return len(strings.Split(s, "\n")) }
