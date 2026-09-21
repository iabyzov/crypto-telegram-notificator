package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newWebhookHandler wires a TelegramWebhookHandler with the given webhook
// secret against the stub Telegram transport.
func newWebhookHandler(t *testing.T, webhookSecret string) (*TelegramWebhookHandler, *stubTelegramTransport) {
	t.Helper()
	handler, _, transport := newTestHandlerWithWebhookSecret(t, webhookSecret)
	return handler, transport
}

// newWebhookRequest builds a POST /webhook request carrying a Telegram update
// with the given secret-token header, as Telegram would deliver it.
func newWebhookRequest(secretToken string) *http.Request {
	body := `{"message":{"message_id":1,"chat":{"id":123},"text":"/help","entities":[{"type":"bot_command","offset":0,"length":5}]}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	if secretToken != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secretToken)
	}
	return req
}

// waitForSend polls the stub transport until at least one message is sent or
// the deadline passes; HandleWebhook dispatches handling in a goroutine.
func waitForSend(t *testing.T, transport *stubTelegramTransport) bool {
	t.Helper()
	// Rejected requests are answered synchronously and never dispatched, so a
	// short window is enough to prove their absence.
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(transport.sentTexts()) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestHandleWebhookSecret(t *testing.T) {
	tests := []struct {
		name          string
		webhookSecret string
		headerToken   string
		wantStatus    int
		wantHandled   bool
	}{
		{
			name:          "matching secret token accepted",
			webhookSecret: "s3cret",
			headerToken:   "s3cret",
			wantStatus:    http.StatusOK,
			wantHandled:   true,
		},
		{
			name:          "missing secret token rejected",
			webhookSecret: "s3cret",
			headerToken:   "",
			wantStatus:    http.StatusUnauthorized,
			wantHandled:   false,
		},
		{
			name:          "wrong secret token rejected",
			webhookSecret: "s3cret",
			headerToken:   "not-the-secret",
			wantStatus:    http.StatusUnauthorized,
			wantHandled:   false,
		},
		{
			name:          "empty configured secret skips check",
			webhookSecret: "",
			headerToken:   "",
			wantStatus:    http.StatusOK,
			wantHandled:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, transport := newWebhookHandler(t, tt.webhookSecret)

			recorder := httptest.NewRecorder()
			handler.HandleWebhook(recorder, newWebhookRequest(tt.headerToken))

			if recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}

			handled := waitForSend(t, transport)
			if handled != tt.wantHandled {
				t.Errorf("message dispatched = %v, want %v (sent texts: %v)", handled, tt.wantHandled, transport.sentTexts())
			}
		})
	}
}
