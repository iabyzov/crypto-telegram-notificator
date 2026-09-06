package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// fakeAlertsRepository records alerts passed to AddAlert.
type fakeAlertsRepository struct {
	mu     sync.Mutex
	alerts []alerts.PriceAlert
}

func (f *fakeAlertsRepository) AddAlert(_ context.Context, alert alerts.PriceAlert) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, alert)
}

func (f *fakeAlertsRepository) GetAlertsByUserID(_ context.Context, _ int64) ([]alerts.PriceAlert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]alerts.PriceAlert(nil), f.alerts...), nil
}

func (f *fakeAlertsRepository) DeleteAlert(_ context.Context, _ alerts.PriceAlert) error {
	return nil
}

func (f *fakeAlertsRepository) recorded() []alerts.PriceAlert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]alerts.PriceAlert(nil), f.alerts...)
}

// stubTelegramTransport intercepts BotAPI sends via the library's HTTPClient
// seam and records the outgoing message text.
type stubTelegramTransport struct {
	mu       sync.Mutex
	messages []stubSentMessage
}

type stubSentMessage struct {
	ChatID int64
	Text   string
}

func (s *stubTelegramTransport) Do(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
		respBody := io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))
		return &http.Response{StatusCode: http.StatusOK, Body: respBody, Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}

	var chatID int64
	if v := form.Get("chat_id"); v != "" {
		chatID, _ = json.Number(v).Int64()
	}

	s.mu.Lock()
	s.messages = append(s.messages, stubSentMessage{ChatID: chatID, Text: form.Get("text")})
	s.mu.Unlock()

	respBody := io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))
	return &http.Response{StatusCode: http.StatusOK, Body: respBody, Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
}

func (s *stubTelegramTransport) sentTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	texts := make([]string, len(s.messages))
	for i, m := range s.messages {
		texts[i] = m.Text
	}
	return texts
}

// newTestHandler wires a TelegramWebhookHandler against a fake repository and a
// stub Telegram transport. The transport is injected through the library's
// HTTPClient seam (NewBotAPIWithClient), so sendMessage goes through the real
// BotAPI code path and its output is observable.
func newTestHandler(t *testing.T) (*TelegramWebhookHandler, *fakeAlertsRepository, *stubTelegramTransport) {
	t.Helper()

	transport := &stubTelegramTransport{}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, transport)
	if err != nil {
		t.Fatalf("creating bot with stub transport: %v", err)
	}

	repo := &fakeAlertsRepository{}
	handler := NewTelegramWebhookHandler(bot, repo, nil)
	return handler, repo, transport
}

// newCommandMessage builds a tgbotapi.Message for a command with arguments,
// as the Telegram webhook would deliver it (bot_command entity at offset 0).
func newCommandMessage(chatID int64, command, arguments string) *tgbotapi.Message {
	return &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: chatID},
		Text: "/" + command + " " + arguments,
		Entities: []tgbotapi.MessageEntity{
			{Type: "bot_command", Offset: 0, Length: len(command) + 1},
		},
	}
}

func sentMessage(t *testing.T, transport *stubTelegramTransport) string {
	t.Helper()
	texts := transport.sentTexts()
	if len(texts) != 1 {
		t.Fatalf("want exactly 1 message sent, got %d: %q", len(texts), texts)
	}
	return texts[0]
}

func TestHandleSetAlertInvalidAlertTypeCreatesNoAlert(t *testing.T) {
	for _, alertTypeWord := range []string{"banana", "above", "below"} {
		t.Run(alertTypeWord, func(t *testing.T) {
			handler, repo, transport := newTestHandler(t)

			handler.handleMessage(newCommandMessage(42, "setalert", "BTC 100 "+alertTypeWord))

			if got := len(repo.recorded()); got != 0 {
				t.Errorf("invalid alert type %q: want 0 alerts recorded, got %d (%+v)", alertTypeWord, got, repo.recorded())
			}
			rejection := sentMessage(t, transport)
			if !strings.Contains(rejection, "more") || !strings.Contains(rejection, "less") {
				t.Errorf("rejection message should name the valid options more/less, got %q", rejection)
			}
		})
	}
}

func TestHandleSetAlertValidInputCreatesAlert(t *testing.T) {
	for _, tc := range []struct {
		direction string
		wantType  alerts.AlertType
	}{
		{direction: "more", wantType: alerts.More},
		{direction: "less", wantType: alerts.Less},
		{direction: "MORE", wantType: alerts.More},
	} {
		t.Run(tc.direction, func(t *testing.T) {
			handler, repo, transport := newTestHandler(t)

			handler.handleMessage(newCommandMessage(42, "setalert", "BTC 50000 "+tc.direction))

			recorded := repo.recorded()
			if len(recorded) != 1 {
				t.Fatalf("want 1 alert recorded, got %d", len(recorded))
			}
			alert := recorded[0]
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

			confirmation := sentMessage(t, transport)
			if !strings.Contains(confirmation, "BTC") || !strings.Contains(confirmation, "50000") {
				t.Errorf("confirmation message should mention symbol and price, got %q", confirmation)
			}
		})
	}
}
