package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// fakeAlertsRepository is an in-memory AlertsRepository for tests. It mirrors
// the Firestore semantics that matter to the handler: AddAlert assigns a
// unique id, DeleteAlert removes the alert with the matching id, and
// GetAlertsByUserID returns only that user's alerts.
//
// Setting fatalErr makes every operation fail with that error (AddAlert stays
// interface-shaped and records the failure in addFailures instead). Tests
// inject it per-case; there is no global on/off switch.
type fakeAlertsRepository struct {
	// fatalErr, when non-nil, is returned by every operation and makes
	// AddAlert a no-op.
	fatalErr error

	// deleteErr, when non-nil, makes DeleteAlert fail with that error and
	// keep the alert stored. It is the per-case injection for pinning the
	// handler's "Failed to delete alert" branch, which fatalErr cannot
	// reach (it fails the retrieval first).
	deleteErr error

	mu         sync.Mutex
	nextID     int
	stored     []alerts.PriceAlert
	deleteLog  []alerts.PriceAlert
	failureLog []deliveryFailure
	addFailure int
}

func (f *fakeAlertsRepository) AddAlert(_ context.Context, alert alerts.PriceAlert) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fatalErr != nil {
		f.addFailure++
		return
	}
	f.nextID++
	alert.Id = strconv.Itoa(f.nextID)
	f.stored = append(f.stored, alert)
}

func (f *fakeAlertsRepository) getAlerts(ctx context.Context) ([]alerts.PriceAlert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fatalErr != nil {
		return nil, f.fatalErr
	}
	return append([]alerts.PriceAlert(nil), f.stored...), nil
}

func (f *fakeAlertsRepository) GetAllAlerts(ctx context.Context) ([]alerts.PriceAlert, error) {
	return f.getAlerts(ctx)
}

func (f *fakeAlertsRepository) GetAlertsByUserID(ctx context.Context, userID int64) ([]alerts.PriceAlert, error) {
	all, err := f.getAlerts(ctx)
	if err != nil {
		return nil, err
	}
	var userAlerts []alerts.PriceAlert
	for _, alert := range all {
		if alert.UserID == userID {
			userAlerts = append(userAlerts, alert)
		}
	}
	return userAlerts, nil
}

// GetAlertByID returns the alert stored under alertID when it belongs to
// userID, and nil (not an error) when no such alert exists — including when
// the id names another user's alert.
func (f *fakeAlertsRepository) GetAlertByID(ctx context.Context, userID int64, alertID string) (*alerts.PriceAlert, error) {
	all, err := f.getAlerts(ctx)
	if err != nil {
		return nil, err
	}
	for _, alert := range all {
		if alert.Id == alertID && alert.UserID == userID {
			found := alert
			return &found, nil
		}
	}
	return nil, nil
}

func (f *fakeAlertsRepository) DeleteAlert(_ context.Context, alert alerts.PriceAlert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fatalErr != nil {
		return f.fatalErr
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i, existing := range f.stored {
		if existing.Id == alert.Id {
			f.stored = append(f.stored[:i], f.stored[i+1:]...)
			f.deleteLog = append(f.deleteLog, alert)
			return nil
		}
	}
	return fmt.Errorf("alert %q not found", alert.Id)
}

// deliveryFailure is one MarkDeliveryFailed call: which alert failed and at
// what timestamp the checker reported.
type deliveryFailure struct {
	alert    alerts.PriceAlert
	failedAt time.Time
}

// MarkDeliveryFailed records the failure and stamps the stored alert, like
// the Firestore repository writes delivery_failed_at in place.
func (f *fakeAlertsRepository) MarkDeliveryFailed(_ context.Context, alert alerts.PriceAlert, failedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fatalErr != nil {
		return f.fatalErr
	}
	for i, existing := range f.stored {
		if existing.Id == alert.Id {
			f.stored[i].DeliveryFailedAt = failedAt
			f.failureLog = append(f.failureLog, deliveryFailure{alert: alert, failedAt: failedAt})
			return nil
		}
	}
	return fmt.Errorf("alert %q not found", alert.Id)
}

// added returns the alerts successfully stored, in insertion order, with the
// ids assigned by AddAlert.
func (f *fakeAlertsRepository) added() []alerts.PriceAlert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]alerts.PriceAlert(nil), f.stored...)
}

// deleted returns the alerts removed by DeleteAlert.
func (f *fakeAlertsRepository) deleted() []alerts.PriceAlert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]alerts.PriceAlert(nil), f.deleteLog...)
}

// deliveryFailures returns the MarkDeliveryFailed calls, in call order.
func (f *fakeAlertsRepository) deliveryFailures() []deliveryFailure {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]deliveryFailure(nil), f.failureLog...)
}

// addFailures returns how many AddAlert calls were rejected due to fatalErr.
func (f *fakeAlertsRepository) addFailures() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addFailure
}

// stubTelegramTransport intercepts BotAPI sends via the library's HTTPClient
// seam and records the outgoing messages.
type stubTelegramTransport struct {
	// failNextSends, when positive, makes the next N sendMessage requests
	// fail at the HTTP layer (like a Telegram outage) and records nothing;
	// each attempt is still counted by sendAttempts. Tests set it directly
	// before triggering a send.
	failNextSends int

	// failEverySends makes every sendMessage request fail, forever; used to
	// observe the checker's give-up path.
	failEverySends bool

	// onSend, when set, is invoked at the moment a sendMessage request
	// arrives, before it is answered. Tests use it to observe state at send
	// time (e.g. that the alert is still stored).
	onSend func()

	mu       sync.Mutex
	messages []stubSentMessage
	attempts int
}

// stubSentMessage is one outgoing Telegram message.
type stubSentMessage struct {
	ChatID int64
	Text   string
}

func (s *stubTelegramTransport) Do(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
		respBody := io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))
		return &http.Response{StatusCode: http.StatusOK, Body: respBody, Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	}

	s.mu.Lock()
	s.attempts++
	failing := s.failNextSends > 0 || s.failEverySends
	if s.failNextSends > 0 {
		s.failNextSends--
	}
	hook := s.onSend
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if failing {
		return nil, errors.New("stub: telegram send failed")
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

// sentMessages returns every successfully recorded message, in send order.
// It never panics, whatever the count.
func (s *stubTelegramTransport) sentMessages() []stubSentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubSentMessage(nil), s.messages...)
}

// sendAttempts returns how many sendMessage requests reached the transport,
// including failed ones.
func (s *stubTelegramTransport) sendAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

// wantSentMessages fails the test unless exactly n messages were recorded,
// and returns them. It is the len-based assert helper tests use in place of
// hand-rolled count checks: assert 0, 1, or N sends and read the slice in
// one step, never panicking on a mismatch.
func (s *stubTelegramTransport) wantSentMessages(t *testing.T, n int) []stubSentMessage {
	t.Helper()
	messages := s.sentMessages()
	if len(messages) != n {
		t.Fatalf("want %d message(s) sent, got %d: %q", n, len(messages), sentTextsOf(messages))
	}
	return messages
}

// newTestHarness wires a TelegramWebhookHandler against a fake repository and
// a stub Telegram transport (see newStubBot for how the transport is
// injected), with no webhook secret.
func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	return newTestHarnessWithWebhookSecret(t, "")
}

// newStubBot wraps a stub Telegram transport in the real BotAPI client,
// injecting it through the library's HTTPClient seam
// (NewBotAPIWithClient), so sendMessage goes through the real library code
// path and its output is observable. Every harness builds its bot through
// this one helper: same token, same API endpoint, same failure message.
func newStubBot(t *testing.T, transport *stubTelegramTransport) *tgbotapi.BotAPI {
	t.Helper()
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, transport)
	if err != nil {
		t.Fatalf("creating bot with stub transport: %v", err)
	}
	return bot
}

// newTestHarnessWithWebhookSecret is newTestHarness with a webhook secret.
func newTestHarnessWithWebhookSecret(t *testing.T, webhookSecret string) *testHarness {
	t.Helper()

	transport := &stubTelegramTransport{}
	bot := newStubBot(t, transport)

	repo := &fakeAlertsRepository{}
	handler := NewTelegramWebhookHandler(bot, repo, nil, webhookSecret)
	return &testHarness{handler: handler, repo: repo, transport: transport}
}

// testHarness bundles the handler under test with its fake repository and
// stub Telegram transport, so tests construct one thing and assert on any
// of the three.
type testHarness struct {
	handler   *TelegramWebhookHandler
	repo      *fakeAlertsRepository
	transport *stubTelegramTransport
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

// dispatchCommand runs a command through handleMessage, as HandleWebhook
// would after receiving the update.
func (h *testHarness) dispatchCommand(chatID int64, command, arguments string) {
	h.handler.handleMessage(newCommandMessage(chatID, command, arguments))
}

// sentMessages returns every message successfully sent through the stub
// transport, in send order. It never panics, whatever the count.
func (h *testHarness) sentMessages() []stubSentMessage {
	return h.transport.sentMessages()
}

// sentTexts returns the text of every successfully sent message.
func (h *testHarness) sentTexts() []string {
	return sentTextsOf(h.transport.sentMessages())
}

// sentTextsOf returns the text of each message, for failure messages that
// show what was actually sent.
func sentTextsOf(messages []stubSentMessage) []string {
	texts := make([]string, len(messages))
	for i, m := range messages {
		texts[i] = m.Text
	}
	return texts
}

// wantSentMessages is the harness delegate of the transport's len-based
// assert helper: fail unless exactly n messages were sent, return them.
func (h *testHarness) wantSentMessages(t *testing.T, n int) []stubSentMessage {
	t.Helper()
	return h.transport.wantSentMessages(t, n)
}

// waitForSend polls the stub transport until at least one message is sent or
// the deadline passes; HandleWebhook dispatches handling in a goroutine.
func (h *testHarness) waitForSend() bool {
	// Rejected requests are answered synchronously and never dispatched, so a
	// short window is enough to prove their absence.
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(h.transport.sentMessages()) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// The tests below pin the contract of the tier-0 test harness itself: every
// future ticket rides on the fake repository and stub transport, so a silent
// change to their semantics would quietly invalidate all downstream tests.

func TestFakeAlertsRepositoryInjectsError(t *testing.T) {
	injected := errors.New("firestore unavailable")

	t.Run("error injected fails every operation", func(t *testing.T) {
		repo := &fakeAlertsRepository{fatalErr: injected}
		repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "BTC"})
		if got := repo.addFailures(); got != 1 {
			t.Errorf("addFailures = %d, want 1", got)
		}
		if _, err := repo.GetAlertsByUserID(context.Background(), 1); err != injected {
			t.Errorf("GetAlertsByUserID error = %v, want %v", err, injected)
		}
		if _, err := repo.GetAllAlerts(context.Background()); err != injected {
			t.Errorf("GetAllAlerts error = %v, want %v", err, injected)
		}
		if _, err := repo.GetAlertByID(context.Background(), 1, "x"); err != injected {
			t.Errorf("GetAlertByID error = %v, want %v", err, injected)
		}
		if err := repo.DeleteAlert(context.Background(), alerts.PriceAlert{Id: "x"}); err != injected {
			t.Errorf("DeleteAlert error = %v, want %v", err, injected)
		}
		if got := len(repo.added()); got != 0 {
			t.Errorf("injected error: want 0 alerts stored, got %d", got)
		}
	})

	t.Run("nil error keeps operations working", func(t *testing.T) {
		repo := &fakeAlertsRepository{}
		alert := alerts.PriceAlert{Symbol: "BTC", UserID: 7}
		repo.AddAlert(context.Background(), alert)

		all, err := repo.GetAllAlerts(context.Background())
		if err != nil || len(all) != 1 {
			t.Errorf("GetAllAlerts = %v, %v; want 1 alert, nil", all, err)
		}
		if got := repo.addFailures(); got != 0 {
			t.Errorf("addFailures = %d, want 0", got)
		}
	})
}

// TestFakeAlertsRepositoryGetAlertIDOwnsIDAndUser pins the by-id lookup
// semantics: the owner's id resolves to exactly that alert, another user's
// id or an unknown id answers nil without an error, and a storage failure
// surfaces as an error.
func TestFakeAlertsRepositoryGetAlertByIDOwnsIDAndUser(t *testing.T) {
	repo := &fakeAlertsRepository{}
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "BTC", UserID: 7})
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "ETH", UserID: 8})
	stored := repo.added()

	found, err := repo.GetAlertByID(context.Background(), 7, stored[0].Id)
	if err != nil {
		t.Fatalf("GetAlertByID for the owner: %v", err)
	}
	if found == nil || found.Id != stored[0].Id || found.Symbol != "BTC" || found.UserID != 7 {
		t.Errorf("owner's id must resolve to their alert, got %+v", found)
	}

	foreign, err := repo.GetAlertByID(context.Background(), 8, stored[0].Id)
	if err != nil {
		t.Fatalf("GetAlertByID for a foreign user: %v", err)
	}
	if foreign != nil {
		t.Errorf("another user's alert must be invisible, got %+v", foreign)
	}

	missing, err := repo.GetAlertByID(context.Background(), 7, "no-such-id")
	if err != nil || missing != nil {
		t.Errorf("unknown id must answer nil without error, got (%+v, %v)", missing, err)
	}

	injected := errors.New("firestore unavailable")
	repo.fatalErr = injected
	if got, err := repo.GetAlertByID(context.Background(), 7, stored[0].Id); got != nil || err != injected {
		t.Errorf("storage failure must surface its error, got (%+v, %v)", got, err)
	}
}

func TestFakeAlertsRepositoryAddAssignsUniqueIds(t *testing.T) {
	repo := &fakeAlertsRepository{}
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "BTC"})
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "ETH"})

	added := repo.added()
	if len(added) != 2 {
		t.Fatalf("want 2 alerts stored, got %d", len(added))
	}
	if added[0].Id == "" || added[1].Id == "" || added[0].Id == added[1].Id {
		t.Errorf("AddAlert should assign distinct non-empty ids, got %q and %q", added[0].Id, added[1].Id)
	}
}

func TestFakeAlertsRepositoryGetAlertsByUserIDSeparatesUsers(t *testing.T) {
	repo := &fakeAlertsRepository{}
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "BTC", UserID: 7})
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "ETH", UserID: 8})

	userAlerts, err := repo.GetAlertsByUserID(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetAlertsByUserID error = %v", err)
	}
	if len(userAlerts) != 1 || userAlerts[0].Symbol != "BTC" {
		t.Errorf("user 7 must see only their own BTC alert, got %+v", userAlerts)
	}
}

func TestFakeAlertsRepositoryDeleteRemovesMatchingAlert(t *testing.T) {
	repo := &fakeAlertsRepository{}
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "BTC"})
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "ETH"})
	added := repo.added()

	if err := repo.DeleteAlert(context.Background(), added[0]); err != nil {
		t.Fatalf("DeleteAlert error = %v, want nil", err)
	}

	remaining, err := repo.GetAllAlerts(context.Background())
	if err != nil {
		t.Fatalf("GetAllAlerts error = %v", err)
	}
	if len(remaining) != 1 || remaining[0].Symbol != "ETH" {
		t.Errorf("want only the ETH alert to remain, got %+v", remaining)
	}
	deleted := repo.deleted()
	if len(deleted) != 1 || deleted[0].Id != added[0].Id {
		t.Errorf("want deleted() to record the deleted alert, got %+v", deleted)
	}
}

// TestFakeAlertsRepositoryDeleteErrKeepsAlertStored pins the deleteErr
// injection: a failing DeleteAlert rejects the removal and changes nothing,
// so tests can observe the handler's "keep stored" contract on a failed
// delete.
func TestFakeAlertsRepositoryDeleteErrKeepsAlertStored(t *testing.T) {
	repo := &fakeAlertsRepository{}
	repo.AddAlert(context.Background(), alerts.PriceAlert{Symbol: "BTC", UserID: 7})
	added := repo.added()
	repo.deleteErr = errors.New("delete rejected")

	if err := repo.DeleteAlert(context.Background(), added[0]); err != repo.deleteErr {
		t.Errorf("DeleteAlert error = %v, want %v", err, repo.deleteErr)
	}
	if remaining := repo.added(); len(remaining) != 1 || remaining[0].Id != added[0].Id {
		t.Errorf("a failed delete must keep the alert stored, got %+v", remaining)
	}
	if got := len(repo.deleted()); got != 0 {
		t.Errorf("a failed delete must not be recorded in deleted(), got %d", got)
	}
}

func TestStubTransportSentMessagesNeverPanics(t *testing.T) {
	h := newTestHarness(t)

	if got := len(h.sentMessages()); got != 0 {
		t.Errorf("fresh transport: want 0 messages, got %d", got)
	}

	h.dispatchCommand(1, "help", "")
	h.dispatchCommand(2, "help", "")

	messages := h.sentMessages()
	if len(messages) != 2 {
		t.Fatalf("want 2 messages recorded, got %d: %+v", len(messages), messages)
	}
	if messages[0].ChatID != 1 || messages[1].ChatID != 2 {
		t.Errorf("chat ids out of order or wrong: got %d, %d", messages[0].ChatID, messages[1].ChatID)
	}
}

// TestStubTransportWantSentMessagesAssertsCount pins the len-based assert
// helper future tests use instead of the panicking exactly-one accessor:
// it fails the test (never panics) on any count mismatch and hands back the
// recorded messages on match, whatever the count.
func TestStubTransportWantSentMessagesAssertsCount(t *testing.T) {
	transport := &stubTelegramTransport{}

	if messages := transport.wantSentMessages(t, 0); len(messages) != 0 {
		t.Errorf("wantSentMessages(0) must answer an empty slice, got %+v", messages)
	}

	h := newTestHarness(t)
	h.dispatchCommand(1, "help", "")
	if messages := h.wantSentMessages(t, 1); len(messages) != 1 || messages[0].ChatID != 1 {
		t.Errorf("harness delegate must return the one recorded message, got %+v", messages)
	}
}

func TestStubTransportFailsNextSends(t *testing.T) {
	h := newTestHarness(t)
	h.transport.failNextSends = 2

	h.dispatchCommand(1, "help", "")
	h.dispatchCommand(1, "help", "")

	if got := h.transport.sendAttempts(); got != 2 {
		t.Errorf("sendAttempts = %d, want 2", got)
	}
	if got := len(h.sentMessages()); got != 0 {
		t.Errorf("failed sends must not record messages, got %d: %+v", got, h.sentMessages())
	}

	h.dispatchCommand(1, "help", "")

	if got := h.transport.sendAttempts(); got != 3 {
		t.Errorf("sendAttempts = %d, want 3", got)
	}
	messages := h.sentMessages()
	if len(messages) != 1 || messages[0].ChatID != 1 {
		t.Errorf("third send should succeed and record, got %+v", messages)
	}
}

func TestNewTestHarnessWiresHandlerRepoAndTransport(t *testing.T) {
	h := newTestHarness(t)

	if h.repo == nil || h.transport == nil || h.handler == nil {
		t.Fatal("harness fields must be non-nil")
	}

	h.dispatchCommand(42, "setalert", "BTC 50000 more")

	if got := len(h.repo.added()); got != 1 {
		t.Fatalf("dispatched /setalert must reach the wired repository, got %d alerts", got)
	}
	messages := h.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("dispatched /setalert must reach the wired transport, got %d messages", len(messages))
	}
	if messages[0].ChatID != 42 {
		t.Errorf("reply must go to chat 42, got %d", messages[0].ChatID)
	}
}
