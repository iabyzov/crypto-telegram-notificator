package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
			h := newTestHarnessWithWebhookSecret(t, tt.webhookSecret)

			recorder := httptest.NewRecorder()
			h.handler.HandleWebhook(recorder, newWebhookRequest(tt.headerToken))

			if recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}

			handled := h.waitForSend()
			if handled != tt.wantHandled {
				t.Errorf("message dispatched = %v, want %v (sent texts: %v)", handled, tt.wantHandled, h.sentTexts())
			}
		})
	}
}
