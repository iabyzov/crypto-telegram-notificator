package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// These tests are the first coverage for /deletealert. They pin every
// user-facing outcome of the command — deletion with confirmation,
// not-found (including another user's alert), invalid format, and the two
// failure branches — so the handler can be reworked behind them without
// changing any observable behavior.

// storedAlertID drives /setalert for chatID over the real command path and
// returns the repository-assigned alert id, ready to be used with
// /deletealert.
func storedAlertID(t *testing.T, h *testHarness, chatID int64, setalertArgs string) string {
	t.Helper()
	h.dispatchCommand(chatID, "setalert", setalertArgs)
	stored := h.repo.added()
	if len(stored) != 1 {
		t.Fatalf("want 1 stored alert after /setalert, got %d", len(stored))
	}
	return stored[0].Id
}

func TestHandleDeleteAlertDeletesAndConfirms(t *testing.T) {
	h := newTestHarness(t)
	alertID := storedAlertID(t, h, 7, "BTC 50000 more")

	h.dispatchCommand(7, "deletealert", alertID)

	messages := h.wantSentMessages(t, 2)
	if messages[1].ChatID != 7 {
		t.Errorf("delete confirmation must go to chat 7, got %d", messages[1].ChatID)
	}
	// Stable marker: the confirmation names the symbol and price, never the
	// surrounding sentence.
	if !strings.Contains(messages[1].Text, "BTC") || !strings.Contains(messages[1].Text, "50000") {
		t.Errorf("delete confirmation must restate the alert, got %q", messages[1].Text)
	}
	if remaining := h.repo.added(); len(remaining) != 0 {
		t.Errorf("the deleted alert must be gone from storage, got %+v", remaining)
	}
	deleted := h.repo.deleted()
	if len(deleted) != 1 || deleted[0].Id != alertID {
		t.Errorf("the deleted alert must be recorded once, got %+v", deleted)
	}
}

func TestHandleDeleteAlertUnknownIDSaysNotFound(t *testing.T) {
	h := newTestHarness(t)
	storedAlertID(t, h, 7, "ETH 3000 less") // keep one stored to prove it survives

	h.dispatchCommand(7, "deletealert", "no-such-id")

	messages := h.wantSentMessages(t, 2)
	if messages[1].ChatID != 7 {
		t.Errorf("not-found notice must go to chat 7, got %d", messages[1].ChatID)
	}
	// Stable marker: the semantic core, not the full sentence with its
	// rewordable /listalerts hint.
	if !strings.Contains(messages[1].Text, "not found") {
		t.Errorf("unknown id must be reported as not found, got %q", messages[1].Text)
	}
	if remaining := h.repo.added(); len(remaining) != 1 {
		t.Errorf("an unknown id must delete nothing, got %+v", remaining)
	}
}

func TestHandleDeleteAlertCannotDeleteOtherUsersAlert(t *testing.T) {
	h := newTestHarness(t)
	alertID := storedAlertID(t, h, 7, "BTC 50000 more")

	// User 8 quotes user 7's alert id: same "not found" answer as for any
	// unknown id, and user 7's alert survives untouched.
	h.dispatchCommand(8, "deletealert", alertID)

	messages := h.wantSentMessages(t, 2)
	if messages[1].ChatID != 8 {
		t.Errorf("not-found notice must go to chat 8, got %d", messages[1].ChatID)
	}
	if !strings.Contains(messages[1].Text, "not found") {
		t.Errorf("another user's alert id must be reported as not found, got %q", messages[1].Text)
	}
	if remaining := h.repo.added(); len(remaining) != 1 || remaining[0].Id != alertID {
		t.Errorf("user 7's alert must survive user 8's attempt, got %+v", remaining)
	}
}

func TestHandleDeleteAlertRejectsWrongArgumentCount(t *testing.T) {
	for _, args := range []string{"", "id1 id2"} {
		t.Run(fmt.Sprintf("args %q", args), func(t *testing.T) {
			h := newTestHarness(t)

			h.dispatchCommand(7, "deletealert", args)

			messages := h.wantSentMessages(t, 1)
			if messages[0].ChatID != 7 {
				t.Errorf("rejection must go to chat 7, got %d", messages[0].ChatID)
			}
			// Stable marker: the rejection vocabulary, not the full usage
			// sentence whose hint text is rewordable.
			if !strings.Contains(messages[0].Text, "Invalid format") {
				t.Errorf("wrong argument count must be rejected with the format hint, got %q", messages[0].Text)
			}
			if remaining := h.repo.added(); len(remaining) != 0 {
				t.Errorf("a rejected command must store nothing, got %+v", remaining)
			}
		})
	}
}

func TestHandleDeleteAlertRepositoryFailureSaysRetrievalFailed(t *testing.T) {
	h := newTestHarness(t)
	h.repo.fatalErr = errors.New("firestore unavailable")

	h.dispatchCommand(7, "deletealert", "1")

	messages := h.wantSentMessages(t, 1)
	if messages[0].ChatID != 7 {
		t.Errorf("error notice must go to chat 7, got %d", messages[0].ChatID)
	}
	// Stable marker: the notice names the failed operation, not the
	// rewordable "Please try again later." tail.
	if !strings.Contains(messages[0].Text, "Failed to retrieve") {
		t.Errorf("a lookup failure must be reported as a retrieval failure, got %q", messages[0].Text)
	}
}

func TestHandleDeleteAlertDeleteFailureSaysDeleteFailed(t *testing.T) {
	h := newTestHarness(t)
	h.repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "BTC", TargetPrice: 50000, UserID: 7, Type: alerts.More})
	h.repo.deleteErr = errors.New("delete rejected")

	h.dispatchCommand(7, "deletealert", "1")

	messages := h.wantSentMessages(t, 1)
	if messages[0].ChatID != 7 {
		t.Errorf("error notice must go to chat 7, got %d", messages[0].ChatID)
	}
	// Stable marker: the notice names the failed operation, not the
	// rewordable "Please try again later." tail.
	if !strings.Contains(messages[0].Text, "Failed to delete alert") {
		t.Errorf("a failing delete must be reported as such, got %q", messages[0].Text)
	}
	if remaining := h.repo.added(); len(remaining) != 1 {
		t.Errorf("a failed delete must keep the alert stored, got %+v", remaining)
	}
}
