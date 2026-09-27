package handlers

import (
	"strings"
	"testing"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

func TestHandleSetAlertInvalidAlertTypeCreatesNoAlert(t *testing.T) {
	for _, alertTypeWord := range []string{"banana", "above", "below"} {
		t.Run(alertTypeWord, func(t *testing.T) {
			h := newTestHarness(t)

			h.dispatchCommand(42, "setalert", "BTC 100 "+alertTypeWord)

			if got := len(h.repo.added()); got != 0 {
				t.Errorf("invalid alert type %q: want 0 alerts recorded, got %d (%+v)", alertTypeWord, got, h.repo.added())
			}
			messages := h.sentMessages()
			if len(messages) != 1 {
				t.Fatalf("want 1 rejection message, got %d: %q", len(messages), h.sentTexts())
			}
			if messages[0].ChatID != 42 {
				t.Errorf("rejection must go to chat 42, got %d", messages[0].ChatID)
			}
			if !strings.Contains(messages[0].Text, "more") || !strings.Contains(messages[0].Text, "less") {
				t.Errorf("rejection message should name the valid options more/less, got %q", messages[0].Text)
			}
		})
	}
}

func TestHandleSetAlertValidInputCreatesAlert(t *testing.T) {
	for _, tc := range []struct {
		alertTypeWord string
		wantType      alerts.AlertType
	}{
		{alertTypeWord: "more", wantType: alerts.More},
		{alertTypeWord: "less", wantType: alerts.Less},
		{alertTypeWord: "MORE", wantType: alerts.More},
	} {
		t.Run(tc.alertTypeWord, func(t *testing.T) {
			h := newTestHarness(t)

			h.dispatchCommand(42, "setalert", "BTC 50000 "+tc.alertTypeWord)

			added := h.repo.added()
			if len(added) != 1 {
				t.Fatalf("want 1 alert recorded, got %d", len(added))
			}
			alert := added[0]
			if alert.Symbol != "BTC" {
				t.Errorf("want symbol BTC, got %q", alert.Symbol)
			}
			if alert.TargetPrice != 50000 {
				t.Errorf("want target price 50000, got %v", alert.TargetPrice)
			}
			if alert.UserID != 42 {
				t.Errorf("want user id 42, got %d", alert.UserID)
			}
			if alert.Type != tc.wantType {
				t.Errorf("want type %s, got %s", tc.wantType, alert.Type)
			}

			messages := h.sentMessages()
			if len(messages) != 1 {
				t.Fatalf("want 1 confirmation message, got %d: %q", len(messages), h.sentTexts())
			}
			if messages[0].ChatID != 42 {
				t.Errorf("confirmation must go to chat 42, got %d", messages[0].ChatID)
			}
		})
	}
}
