package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
)

// fakeClock makes the checker's retry waits and timestamping deterministic:
// sleeps record their duration and advance the clock, Now returns the
// recorded instant. Safe for the checker's worker pool: multiple goroutines
// may sleep concurrently.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
	// slept records the durations passed to checkerClock.Sleep, in call order.
	slept []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
}

// checkerHarness wires an AlertChecker against a fake repository, the stub
// Telegram transport, and a fake CoinMarketCap server, so checker behavior is
// observable end to end: what the user was sent, which alerts remain, and
// which delivery timestamps were written.
type checkerHarness struct {
	checker   *AlertChecker
	repo      *fakeAlertsRepository
	transport *stubTelegramTransport
	cmc       *httptest.Server
	clock     *fakeClock

	// cmcMu guards cmcBatches, which the fake server's goroutine appends to.
	cmcMu sync.Mutex
	// cmcBatches holds the symbol batch of every quotes/latest request, in
	// request order: how the checker spent a run's pricing quota.
	cmcBatches [][]string
}

// newCheckerHarness starts a fake CoinMarketCap server that quotes every
// requested symbol at the given price, and wires a checker around it.
func newCheckerHarness(t *testing.T, price float64) *checkerHarness {
	t.Helper()

	h := &checkerHarness{}
	cmc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CoinMarketCap receives the symbols as one comma-separated query
		// param and keys its response by each individual symbol.
		var symbols []string
		for _, param := range r.URL.Query()["symbol"] {
			symbols = append(symbols, strings.Split(param, ",")...)
		}
		h.cmcMu.Lock()
		h.cmcBatches = append(h.cmcBatches, symbols)
		h.cmcMu.Unlock()

		data := make(map[string]map[string]map[string]map[string]float64, len(symbols))
		for _, symbol := range symbols {
			// Mirrors the CoinMarketCap quotes/latest shape:
			// {"data":{"BTC":{"quote":{"USD":{"price":...}}}}}
			data[symbol] = map[string]map[string]map[string]float64{
				"quote": {"USD": {"price": price}},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(cmc.Close)

	transport := &stubTelegramTransport{}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, transport)
	if err != nil {
		t.Fatalf("creating bot with stub transport: %v", err)
	}

	repo := &fakeAlertsRepository{}
	priceService := services.NewPriceServiceWithEndpoint("test-cmc-key", nil, time.Minute, cmc.URL)
	clock := &fakeClock{now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	checker := NewAlertCheckerWithClock(repo, priceService, bot, clock)

	h.checker = checker
	h.repo = repo
	h.transport = transport
	h.cmc = cmc
	h.clock = clock
	return h
}

// cmcBatchesRequested returns the symbol batch of every quotes/latest
// request, in request order.
func (h *checkerHarness) cmcBatchesRequested() [][]string {
	h.cmcMu.Lock()
	defer h.cmcMu.Unlock()
	return append([][]string(nil), h.cmcBatches...)
}

// runCheckWithAlert runs one scheduled check with a single stored alert.
func (h *checkerHarness) runCheckWithAlert(t *testing.T, alert alerts.PriceAlert) {
	t.Helper()
	h.repo.AddAlert(context.Background(), alert)
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}
}

func TestCheckAlertsSendsAndDeletesTriggeredAlert(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	if got := len(h.transport.sentMessages()); got != 1 {
		t.Fatalf("expected exactly one notification, got %d", got)
	}
	if got, want := len(h.repo.added()), 0; got != want {
		t.Errorf("alert should be deleted after successful delivery, %d remain", got)
	}
}

// TestCheckAlertsBatchesAndDedupesSymbols pins a run's pricing quota: all
// live alerts are priced by a single batched CoinMarketCap request that names
// each unique symbol exactly once - alerts sharing a symbol ride the same
// quote - and every triggered alert is delivered, once to its own user.
func TestCheckAlertsBatchesAndDedupesSymbols(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	h.repo.AddAlert(context.Background(), alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 1, Type: alerts.More,
	})
	h.repo.AddAlert(context.Background(), alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 49500, UserID: 2, Type: alerts.More,
	})
	h.repo.AddAlert(context.Background(), alerts.PriceAlert{
		Symbol: "ETH", TargetPrice: 3000, UserID: 3, Type: alerts.More,
	})

	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}

	batches := h.cmcBatchesRequested()
	if got := len(batches); got != 1 {
		t.Fatalf("a run must price all alerts in one batched request, got %d requests", got)
	}
	requested := append([]string(nil), batches[0]...)
	slices.Sort(requested)
	if want := []string{"BTC", "ETH"}; !slices.Equal(requested, want) {
		t.Errorf("priced symbols = %v, want %v: each unique symbol exactly once", requested, want)
	}

	// Every triggered alert is delivered, exactly once to its own user.
	chatCounts := make(map[int64]int)
	for _, m := range h.transport.sentMessages() {
		chatCounts[m.ChatID]++
	}
	for _, chatID := range []int64{1, 2, 3} {
		if got := chatCounts[chatID]; got != 1 {
			t.Errorf("user %d must receive exactly one notification, got %d", chatID, got)
		}
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("all triggered alerts must be deleted after delivery, %d remain", got)
	}
}

// TestCheckAlertsSendFirstOrder pins the at-least-once invariant at the
// delivery boundary: at the moment the notification reaches Telegram, the
// alert is still in storage - it is deleted only after the send succeeds.
// Delete-then-notify permanently loses the alert when the send fails;
// send-first lets the next scheduled run retry it.
func TestCheckAlertsSendFirstOrder(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)

	stillStoredAtSend := false
	h.transport.onSend = func() {
		stillStoredAtSend = len(h.repo.added()) == 1
	}

	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	if got := h.transport.sendAttempts(); got != 1 {
		t.Fatalf("expected exactly 1 send attempt for a delivered alert, got %d", got)
	}
	if !stillStoredAtSend {
		t.Error("alert must still be stored when the notification is sent; delete must happen only after a successful send")
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("alert should be deleted after successful delivery, %d remain", got)
	}
}

func TestCheckAlertsNotificationRetrySucceedsAfterTwoFailures(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	// The first two attempts fail like a Telegram outage; the third succeeds.
	h.transport.failNextSends = 2
	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	if got, want := h.transport.sendAttempts(), 3; got != want {
		t.Errorf("expected %d send attempts, got %d", want, got)
	}
	if got := len(h.transport.sentMessages()); got != 1 {
		t.Fatalf("notification must reach the user on the third attempt, got %d messages", got)
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("alert must be deleted after successful delivery, %d remain", got)
	}
	// The waits between the three failed-then-successful attempts are the
	// ticket's exponential backoff: 2s after the first failure, 4s after the
	// second.
	if got, want := h.clock.slept, []time.Duration{2 * time.Second, 4 * time.Second}; len(got) != len(want) {
		t.Fatalf("expected waits %v, got %v", want, got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("wait %d: expected %v, got %v", i, want[i], got[i])
			}
		}
	}
	// No delivery failure was persisted: the alert was delivered on this run.
	if got := len(h.repo.deliveryFailures()); got != 0 {
		t.Errorf("no delivery failure should be recorded for a delivered alert, got %d", got)
	}
}

func TestCheckAlertsNotificationFailureWritesDeliveryFailedAt(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	h.transport.failEverySends = true
	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	// Immediate attempt plus three backoff retries (2s/4s/8s), then give up.
	if got, want := h.transport.sendAttempts(), 4; got != want {
		t.Errorf("expected %d send attempts, got %d", want, got)
	}
	// The alert stays in storage for the next scheduler run to retry.
	if got := len(h.repo.added()); got != 1 {
		t.Fatalf("undelivered alert must be kept for the next run, %d remain", got)
	}
	failures := h.repo.deliveryFailures()
	if len(failures) != 1 {
		t.Fatalf("expected exactly one delivery failure record, got %d", len(failures))
	}
	if got, want := failures[0].alert.Id, h.repo.added()[0].Id; got != want {
		t.Errorf("delivery failure recorded for alert %q, want %q", got, want)
	}
	if failures[0].failedAt.IsZero() {
		t.Error("delivery failure timestamp must not be zero")
	}
	// Backoff waits between the four attempts: 2s, 4s, 8s.
	if got, want := len(h.clock.slept), 3; got != want {
		t.Errorf("expected %d backoff waits, got %d", want, got)
	}
}

// TestCheckAlertsKeptAlertIsDeliveredOnNextRun pins the full at-least-once
// cycle: an alert that fails every attempt on one run (kept, stamped) is
// delivered and deleted by the next scheduled run once Telegram recovers.
func TestCheckAlertsKeptAlertIsDeliveredOnNextRun(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	h.repo.AddAlert(context.Background(), alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	h.transport.failEverySends = true
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("first CheckAlerts: %v", err)
	}
	if got := len(h.repo.added()); got != 1 {
		t.Fatalf("after a fully failed run the alert must be kept, %d remain", got)
	}
	firstRunAttempts := h.transport.sendAttempts()

	// Telegram recovers; the scheduler fires the next run.
	h.transport.failEverySends = false
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("second CheckAlerts: %v", err)
	}

	// The kept alert is redelivered: exactly one more send attempt, it
	// succeeded, and the alert is gone.
	if got, want := h.transport.sendAttempts(), firstRunAttempts+1; got != want {
		t.Errorf("expected %d total send attempts across both runs, got %d", want, got)
	}
	if got := len(h.transport.sentMessages()); got != 1 {
		t.Fatalf("notification must reach the user on the second run, got %d messages", got)
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("alert must be deleted after redelivery, %d remain", got)
	}
}

// TestCheckAlertsUntriggeredAlertSurvives pins that the retry path only
// touches triggered alerts: a non-triggered alert is never sent, deleted, or
// marked failed.
func TestCheckAlertsUntriggeredAlertSurvives(t *testing.T) {
	h := newCheckerHarness(t, 49000.0)
	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	if got := h.transport.sendAttempts(); got != 0 {
		t.Errorf("untriggered alert must not be notified, got %d attempts", got)
	}
	if got := len(h.repo.added()); got != 1 {
		t.Errorf("untriggered alert must be kept, got %d remaining", got)
	}
	if got := len(h.repo.deliveryFailures()); got != 0 {
		t.Errorf("untriggered alert must not be marked failed, got %d", got)
	}
}
