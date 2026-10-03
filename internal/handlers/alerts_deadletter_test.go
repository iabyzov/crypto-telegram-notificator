package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// capturingLogHandler records every slog record handed to it so tests can
// assert on the dead-letter ERROR emission without depending on log output
// formatting.
type capturingLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingLogHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *capturingLogHandler) errorRecords() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var errs []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelError {
			errs = append(errs, r)
		}
	}
	return errs
}

// captureLogs swaps slog's default logger for a capturing handler for the
// duration of the test and returns the handler.
func captureLogs(t *testing.T) *capturingLogHandler {
	t.Helper()
	handler := &capturingLogHandler{}
	original := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(original) })
	return handler
}

// recordAttr returns the value of the named attribute on a record formatted
// as a string, or "" when absent. Handles the attribute kinds the dead-letter
// record carries: string, int64 (user_id), and float64 (target_price).
func recordAttr(r slog.Record, key string) string {
	var value string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != key {
			return true
		}
		switch v := a.Value.Any().(type) {
		case string:
			value = v
		case int64:
			value = strconv.FormatInt(v, 10)
		case float64:
			value = strconv.FormatFloat(v, 'f', -1, 64)
		}
		return true
	})
	return value
}

// storedAlert returns the single stored alert, failing the test on any other
// count.
func storedAlert(t *testing.T, h *checkerHarness) alerts.PriceAlert {
	t.Helper()
	stored := h.repo.added()
	if len(stored) != 1 {
		t.Fatalf("expected exactly one stored alert, got %d", len(stored))
	}
	return stored[0]
}

// storeStampedAlert adds one alert pre-stamped with a delivery failure at
// failedAt, as a previous checker run would have left it.
func (h *checkerHarness) storeStampedAlert(t *testing.T, alert alerts.PriceAlert, failedAt time.Time) {
	t.Helper()
	alert.DeliveryFailedAt = failedAt
	h.repo.AddAlert(context.Background(), alert)
}

// TestCheckAlertsDeadLettersAlertFailingOverAnHour pins the ticket's core
// behavior: an alert whose delivery has been failing for more than an hour is
// terminally dead-lettered - deleted from storage, counted in
// telegram_notification_errors_total, and reported via an slog ERROR with the
// alert's details. No further delivery is attempted.
func TestCheckAlertsDeadLettersAlertFailingOverAnHour(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	logs := captureLogs(t)

	// Failing for 2 hours: well past the 1-hour deadline, and it still
	// triggers at the current price.
	h.storeStampedAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	}, h.clock.now.Add(-2*time.Hour))

	counterBefore := testutil.ToFloat64(telegramNotificationErrors)
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}

	// Terminally deleted - the alert does not linger.
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("dead-lettered alert must be deleted, %d remain", got)
	}
	// No delivery attempt: dead-lettering is terminal, not another retry.
	if got := h.transport.sendAttempts(); got != 0 {
		t.Errorf("dead-lettered alert must not be notified, got %d attempts", got)
	}
	// The counter increments on dead-lettering.
	if got := testutil.ToFloat64(telegramNotificationErrors) - counterBefore; got != 1 {
		t.Errorf("dead-lettering must increment telegram_notification_errors_total by 1, got %v", got)
	}
	// The loss is loud: one ERROR record carrying the alert's identity.
	errs := logs.errorRecords()
	if len(errs) != 1 {
		t.Fatalf("expected exactly one ERROR log record, got %d", len(errs))
	}
	deleted := h.repo.deleted()
	if len(deleted) != 1 {
		t.Fatalf("expected exactly one deleted alert, got %d", len(deleted))
	}
	// The record carries every detail the ADR promises the operator: id,
	// user, symbol, target price, type, and the first-failure time.
	want := map[string]string{
		"alert_id":        deleted[0].Id,
		"user_id":         "42",
		"symbol":          "BTC",
		"target_price":    "50000",
		"alert_type":      "More",
		"first_failed_at": h.clock.now.Add(-2 * time.Hour).Format(time.RFC3339),
	}
	for key, wantValue := range want {
		if got := recordAttr(errs[0], key); got != wantValue {
			t.Errorf("ERROR record %s = %q, want %q", key, got, wantValue)
		}
	}
}

// TestCheckAlertsDeadLettersAgedAlertEvenWhenUntriggered pins that the
// 1-hour window is keyed on age alone: a stamped alert whose price has
// retraced (no longer triggering) is still dead-lettered, so failures do not
// linger invisibly in storage waiting for the condition to recur.
func TestCheckAlertsDeadLettersAgedAlertEvenWhenUntriggered(t *testing.T) {
	h := newCheckerHarness(t, 1000.0) // price far below the More target

	h.storeStampedAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	}, h.clock.now.Add(-90*time.Minute))

	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}

	if got := len(h.repo.added()); got != 0 {
		t.Errorf("aged untriggered alert must be dead-lettered, %d remain", got)
	}
	if got := h.transport.sendAttempts(); got != 0 {
		t.Errorf("dead-lettered alert must not be notified, got %d attempts", got)
	}
}

// TestCheckAlertsKeepsRecentlyFailedAlertForRedelivery pins the other side of
// the window: a failure younger than 1 hour is NOT dead-lettered. The alert
// continues through the normal at-least-once flow - here it triggers and
// delivers successfully.
func TestCheckAlertsKeepsRecentlyFailedAlertForRedelivery(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)

	h.storeStampedAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	}, h.clock.now.Add(-30*time.Minute))

	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}

	if got := h.transport.sendAttempts(); got != 1 {
		t.Errorf("recently failed alert must be redelivered (1 attempt), got %d", got)
	}
	if got := len(h.transport.sentMessages()); got != 1 {
		t.Errorf("notification must reach the user, got %d messages", got)
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("delivered alert must be deleted, %d remain", got)
	}
}

// TestCheckAlertsKeepsEarliestFailureStamp pins the first-failure semantics:
// the first failing run stamps delivery_failed_at; later failing runs keep
// that earliest stamp, so the 1-hour deadline measures from the first
// failure, not the most recent one.
func TestCheckAlertsKeepsEarliestFailureStamp(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	h.transport.failEverySends = true

	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	stamped := storedAlert(t, h)
	if stamped.DeliveryFailedAt.IsZero() {
		t.Fatal("first failing run must stamp delivery_failed_at")
	}
	firstStamp := stamped.DeliveryFailedAt

	// The next scheduled run fails again 10 minutes later.
	h.clock.now = h.clock.now.Add(10 * time.Minute)
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("second CheckAlerts: %v", err)
	}

	restamped := storedAlert(t, h)
	if !restamped.DeliveryFailedAt.Equal(firstStamp) {
		t.Errorf("earliest failure stamp must be kept, want %v, got %v",
			firstStamp, restamped.DeliveryFailedAt)
	}
}

// TestCheckAlertsCountsFailedNotificationAttempts pins the counter contract
// from ticket #33: every failed notification attempt increments
// telegram_notification_errors_total, not just dead-lettering. One fully
// failed delivery cycle makes four attempts.
func TestCheckAlertsCountsFailedNotificationAttempts(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	h.transport.failEverySends = true

	counterBefore := testutil.ToFloat64(telegramNotificationErrors)
	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	if got, want := testutil.ToFloat64(telegramNotificationErrors)-counterBefore, 4.0; got != want {
		t.Errorf("four failed attempts must add %v to telegram_notification_errors_total, got %v", want, got)
	}
}

// TestCheckAlertsDoesNotDeadLetterUnstampedAlerts guards the sweep boundary:
// an alert that has never failed delivery never enters the dead-letter path,
// whatever its trigger state.
func TestCheckAlertsDoesNotDeadLetterUnstampedAlerts(t *testing.T) {
	h := newCheckerHarness(t, 1000.0)

	h.runCheckWithAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})

	if got := len(h.repo.added()); got != 1 {
		t.Errorf("unstamped alert must be kept through the normal flow, %d remain", got)
	}
}

// flakyDeleteRepository wraps the fake repository and fails the first N
// DeleteAlert calls, driving the checker's terminal-delete failure path
// deterministically - the storage equivalent of a Firestore outage at the
// exact moment a dead alert is being swept.
type flakyDeleteRepository struct {
	*fakeAlertsRepository
	failures int
}

func (f *flakyDeleteRepository) DeleteAlert(ctx context.Context, alert alerts.PriceAlert) error {
	if f.failures > 0 {
		f.failures--
		return errors.New("injected: delete unavailable")
	}
	return f.fakeAlertsRepository.DeleteAlert(ctx, alert)
}

// newCheckerOverRepo wires an AlertChecker over an arbitrary repository
// (sharing the harness's fake CoinMarketCap server, telegram transport, and
// clock), so a run can be driven through a repository with injected faults.
func (h *checkerHarness) newCheckerOverRepo(t *testing.T, repo AlertsRepository) *AlertChecker {
	t.Helper()
	return NewAlertCheckerWithClock(
		repo,
		services.NewPriceServiceWithEndpoint("test-cmc-key", nil, time.Minute, h.cmc.URL),
		newStubBot(t, h.transport),
		h.clock,
	)
}

// TestCheckAlertsKeepsAlertExactlyOneHourOld pins the exclusive side of the
// deadline: an alert that failed exactly one hour ago is NOT yet
// dead-lettered (> 1 hour is terminal), and continues through the normal
// at-least-once flow instead.
func TestCheckAlertsKeepsAlertExactlyOneHourOld(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)
	logs := captureLogs(t)

	h.storeStampedAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	}, h.clock.now.Add(-deliveryFailureDeadline)) // exactly one hour

	counterBefore := testutil.ToFloat64(telegramNotificationErrors)
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}

	// Not dead-lettered: the normal flow delivered it.
	if got := h.transport.sendAttempts(); got != 1 {
		t.Errorf("alert exactly one hour old must be redelivered (1 attempt), got %d", got)
	}
	if got := len(h.transport.sentMessages()); got != 1 {
		t.Errorf("notification must reach the user, got %d messages", got)
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("delivered alert must be deleted, %d remain", got)
	}
	if got := testutil.ToFloat64(telegramNotificationErrors) - counterBefore; got != 0 {
		t.Errorf("no dead-letter accounting for an exactly-one-hour-old alert, counter moved by %v", got)
	}
	if got := len(logs.errorRecords()); got != 0 {
		t.Errorf("no dead-letter ERROR record for an exactly-one-hour-old alert, got %d", got)
	}
}

// TestCheckAlertsFailedTerminalDeleteKeepsAlertForNextSweep pins the
// retry-on-delete-failure contract from ADR-0001: when the terminal delete
// itself fails, the alert is kept with NO dead-letter accounting (no ERROR
// record, no counter increment), and the next scheduled run's sweep retries
// the dead-letter to completion.
func TestCheckAlertsFailedTerminalDeleteKeepsAlertForNextSweep(t *testing.T) {
	h := newCheckerHarness(t, 1000.0) // price retraced: the aged alert does not trigger
	logs := captureLogs(t)

	h.storeStampedAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	}, h.clock.now.Add(-2*time.Hour))

	flaky := &flakyDeleteRepository{fakeAlertsRepository: h.repo, failures: 1}
	failingRun := h.newCheckerOverRepo(t, flaky)

	counterBefore := testutil.ToFloat64(telegramNotificationErrors)
	if err := failingRun.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts (failed delete run): %v", err)
	}

	// The alert survived the failed terminal delete, uncounted and unlogged.
	if got := len(h.repo.added()); got != 1 {
		t.Fatalf("alert must be kept when the terminal delete fails, %d remain", got)
	}
	if got := len(logs.errorRecords()); got != 0 {
		t.Errorf("failed delete must not emit a dead-letter ERROR record, got %d", got)
	}
	if got := testutil.ToFloat64(telegramNotificationErrors) - counterBefore; got != 0 {
		t.Errorf("failed delete must not touch the counter, moved by %v", got)
	}
	if got := h.transport.sendAttempts(); got != 0 {
		t.Errorf("untriggered kept alert must not be notified, got %d attempts", got)
	}

	// The next run's sweep retries the dead-letter and completes it.
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts (retry run): %v", err)
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("retry run must dead-letter the alert, %d remain", got)
	}
	if got := testutil.ToFloat64(telegramNotificationErrors) - counterBefore; got != 1 {
		t.Errorf("completed dead-letter must increment the counter by 1, got %v", got)
	}
	if got := len(logs.errorRecords()); got != 1 {
		t.Errorf("completed dead-letter must emit one ERROR record, got %d", got)
	}
}

// TestCheckAlertsSweepSpendsNoQuotaOnDeadAlerts pins the ADR's quota choice:
// the dead-letter sweep runs before price fetching, so a run whose only alert
// is dead-lettered never reaches the pricing API at all. The alert would
// trigger at the quoted price - only its age takes it out of the run, which
// is what proves the sweep, not the trigger filter, kept it from pricing.
func TestCheckAlertsSweepSpendsNoQuotaOnDeadAlerts(t *testing.T) {
	h := newCheckerHarness(t, 51000.0)

	h.storeStampedAlert(t, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	}, h.clock.now.Add(-2*time.Hour))

	counterBefore := testutil.ToFloat64(telegramNotificationErrors)
	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts: %v", err)
	}

	if got := len(h.cmcBatchesRequested()); got != 0 {
		t.Errorf("dead-lettered alerts must spend no pricing quota, got %d CoinMarketCap requests", got)
	}
	if got := len(h.repo.added()); got != 0 {
		t.Errorf("aged alert must be dead-lettered, %d remain", got)
	}
	if got := h.transport.sendAttempts(); got != 0 {
		t.Errorf("dead-lettered alert must not be notified, got %d attempts", got)
	}
	if got := testutil.ToFloat64(telegramNotificationErrors) - counterBefore; got != 1 {
		t.Errorf("dead-lettering must increment the counter by 1, got %v", got)
	}
}
