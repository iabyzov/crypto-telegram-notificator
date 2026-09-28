package handlers

import (
	"errors"
	"strings"
	"testing"
)

// These tests are the first consumers riding on the hardened harness's two
// headline capabilities: repository error injection (repo.fatalErr) and
// multi-message capture (sentMessages). They drive real handler behavior,
// the way future tickets will use the harness.

// TestListAlertsWithRepositoryFailureSendsErrorToUser injects a repository
// failure and proves the handler's user-facing error path: the user is told
// the retrieval failed, nothing is stored, and clearing the injection
// restores normal behavior in the same harness.
func TestListAlertsWithRepositoryFailureSendsErrorToUser(t *testing.T) {
	h := newTestHarness(t)
	h.repo.fatalErr = errors.New("firestore unavailable")

	h.dispatchCommand(7, "listalerts", "")

	messages := h.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("want 1 error notice, got %d: %q", len(messages), h.sentTexts())
	}
	if messages[0].ChatID != 7 {
		t.Errorf("error notice must go to chat 7, got %d", messages[0].ChatID)
	}
	if !strings.Contains(messages[0].Text, "Failed to retrieve alerts") {
		t.Errorf("user must be told the retrieval failed, got %q", messages[0].Text)
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("want no alerts stored while the repository is failing, got %d", got)
	}

	// The injection is per-case: with fatalErr cleared the same handler and
	// repository serve the normal path again, proving the failure did not
	// leave anything broken behind.
	h.repo.fatalErr = nil
	h.dispatchCommand(7, "listalerts", "")

	messages = h.sentMessages()
	if len(messages) != 2 {
		t.Fatalf("after recovery want 2 replies total, got %d: %q", len(messages), h.sentTexts())
	}
	if messages[1].ChatID != 7 {
		t.Errorf("recovery reply must go to chat 7, got %d", messages[1].ChatID)
	}
	if !strings.Contains(messages[1].Text, "You have no active alerts") {
		t.Errorf("recovered /listalerts must report no alerts, got %q", messages[1].Text)
	}
}

// TestSetAlertThenListAlertsConversationSendsEveryReply proves multi-message
// capture on a realistic conversation: every reply is recorded in send order
// with its chat id, the id the repository assigned flows into the user's
// list, and another user's chat sees none of it.
func TestSetAlertThenListAlertsConversationSendsEveryReply(t *testing.T) {
	h := newTestHarness(t)

	h.dispatchCommand(7, "setalert", "BTC 50000 more")
	h.dispatchCommand(7, "listalerts", "")
	h.dispatchCommand(8, "listalerts", "")

	messages := h.sentMessages()
	if len(messages) != 3 {
		t.Fatalf("a 3-command conversation must record every reply, got %d: %q", len(messages), h.sentTexts())
	}
	if messages[0].ChatID != 7 || messages[1].ChatID != 7 || messages[2].ChatID != 8 {
		t.Errorf("replies must keep their chat ids in order: got %d, %d, %d",
			messages[0].ChatID, messages[1].ChatID, messages[2].ChatID)
	}
	if !strings.Contains(messages[0].Text, "Alert set for BTC at $50000.00") {
		t.Errorf("first reply must confirm the alert, got %q", messages[0].Text)
	}

	stored := h.repo.added()
	if len(stored) != 1 {
		t.Fatalf("want 1 stored alert, got %d", len(stored))
	}
	if !strings.Contains(messages[1].Text, "Your active alerts") ||
		!strings.Contains(messages[1].Text, "BTC") ||
		!strings.Contains(messages[1].Text, stored[0].Id) {
		t.Errorf("second reply must list the stored alert with its id %q, got %q", stored[0].Id, messages[1].Text)
	}
	if !strings.Contains(messages[2].Text, "You have no active alerts") {
		t.Errorf("user separation: chat 8 must see no alerts, got %q", messages[2].Text)
	}
}