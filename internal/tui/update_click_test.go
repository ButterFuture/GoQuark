package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// updateConfirmModel is the dialog as it stands when the user is about to
// click 立即更新.
func updateConfirmModel() model {
	m := model{width: 100, height: 40, modalBack: modeBrowse}
	m.updateRes = &update.Result{
		Current: "1.0.4", Latest: "1.0.5", CanApply: true,
		AssetName: "goquark_1.0.5_darwin_arm64",
		Notes:     "### Fixed\n- something",
	}
	return m.showConfirm("发现新版本", m.updateConfirmBody(), "update_available")
}

func findHit(t *testing.T, m model, hit func(int, int) bool, what string) (int, int) {
	t.Helper()
	for y := 0; y < m.height; y++ {
		for x := 0; x < m.width; x++ {
			if hit(x, y) {
				return x, y
			}
		}
	}
	t.Fatalf("%s not found on screen", what)
	return 0, 0
}

func click(m model, x, y int, action tea.MouseAction) model {
	next, _ := m.Update(tea.MouseMsg{
		X: x, Y: y, Button: tea.MouseButtonLeft, Action: action,
	})
	return next.(model)
}

func press(m model, key tea.KeyType) model {
	next, _ := m.Update(tea.KeyMsg{Type: key})
	return next.(model)
}

// Clicking 立即更新 must leave the progress dialog on screen. A click is a
// press followed by a release: the press starts the update and swaps in the
// progress dialog, and the release was then read by that new dialog as "click
// anywhere to dismiss", so it vanished in the frame it appeared.
func TestClickingUpdateKeepsTheProgressDialog(t *testing.T) {
	m := updateConfirmModel()
	x, y := findHit(t, m, m.hitOK, "立即更新 button")

	m = click(m, x, y, tea.MouseActionPress)
	if m.mode != modeModal || m.modalKind != "progress" {
		t.Fatalf("press did not start the update: mode=%v kind=%q", m.mode, m.modalKind)
	}

	m = click(m, x, y, tea.MouseActionRelease)
	if m.mode != modeModal || m.modalKind != "progress" {
		t.Fatalf("the release dismissed the progress dialog: mode=%v kind=%q", m.mode, m.modalKind)
	}
	if m.modalTitle != "正在更新" {
		t.Errorf("progress dialog replaced: title=%q", m.modalTitle)
	}
}

// Nor may any later stray click dismiss it — the download is still running.
func TestStrayClickDoesNotDismissTheProgressDialog(t *testing.T) {
	m := updateConfirmModel()
	x, y := findHit(t, m, m.hitOK, "立即更新 button")
	m = click(m, x, y, tea.MouseActionPress)
	m = click(m, x, y, tea.MouseActionRelease)

	// A corner of the screen, well away from the dialog.
	m = click(m, 0, 0, tea.MouseActionPress)
	m = click(m, 0, 0, tea.MouseActionRelease)
	if m.mode != modeModal || m.modalKind != "progress" {
		t.Fatalf("a stray click dismissed the progress dialog: mode=%v kind=%q", m.mode, m.modalKind)
	}
}

func startedUpdate(t *testing.T) (model, *bool) {
	t.Helper()
	m := updateConfirmModel()
	x, y := findHit(t, m, m.hitOK, "立即更新 button")
	m = click(m, x, y, tea.MouseActionPress)
	m = click(m, x, y, tea.MouseActionRelease)

	cancelled := false
	m.updateCancel = func() { cancelled = true }
	return m, &cancelled
}

// The cancel button has to actually cancel, not merely hide the dialog.
func TestProgressCancelButtonCancelsTheDownload(t *testing.T) {
	m, cancelled := startedUpdate(t)
	x, y := findHit(t, m, m.progressCancelHit, progressCancelLabel+" button")

	m = click(m, x, y, tea.MouseActionPress)
	if !*cancelled {
		t.Fatal("clicking the cancel button did not cancel the download")
	}
	if m.mode != modeModal {
		t.Error("the dialog closed before the goroutine reported back")
	}
}

func TestEscCancelsTheDownload(t *testing.T) {
	m, cancelled := startedUpdate(t)
	m = press(m, tea.KeyEsc)
	if !*cancelled {
		t.Fatal("Esc did not cancel the download")
	}
}

// Enter is how every other dialog is dismissed, so it must not be the way a
// half-finished download gets thrown away.
func TestEnterDoesNotCancelTheDownload(t *testing.T) {
	m, cancelled := startedUpdate(t)
	m = press(m, tea.KeyEnter)
	if *cancelled {
		t.Error("Enter cancelled the download")
	}
	if m.modalKind != "progress" {
		t.Errorf("Enter dismissed the progress dialog: kind=%q", m.modalKind)
	}
}

// Once the download is done there is nothing left to cancel, so the button
// goes away.
func TestCancelButtonGoesAwayWhenTheUpdateFinishes(t *testing.T) {
	m, _ := startedUpdate(t)
	if !strings.Contains(m.renderModal(), progressCancelLabel) {
		t.Fatal("no cancel button while downloading")
	}

	next, _ := m.Update(updateApplyMsg{res: m.updateRes})
	m = next.(model)

	if m.modalKind == "progress" {
		t.Fatal("still showing the progress dialog after the update finished")
	}
	if strings.Contains(m.renderModal(), progressCancelLabel) {
		t.Error("cancel button still on screen after the update finished")
	}
	if m.modalTitle != "更新完成" {
		t.Errorf("title = %q, want 更新完成", m.modalTitle)
	}
}

// A cancelled download is not a failure, and nothing was installed.
func TestCancelledUpdateReportsItselfAsCancelled(t *testing.T) {
	m, _ := startedUpdate(t)
	next, _ := m.Update(updateApplyMsg{res: m.updateRes, err: context.Canceled})
	m = next.(model)

	if m.modalKind == "error" {
		t.Error("a cancelled update was reported as a failure")
	}
	if m.modalTitle != "已取消更新" {
		t.Errorf("title = %q, want 已取消更新", m.modalTitle)
	}
}
