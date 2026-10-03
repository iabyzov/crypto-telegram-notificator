package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/handlers"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMustEnvReturnsSetValue(t *testing.T) {
	t.Setenv("MUSTENV_TEST_VAR", "some-value")

	if got := mustEnv("MUSTENV_TEST_VAR"); got != "some-value" {
		t.Fatalf("mustEnv(%q) = %q, want %q", "MUSTENV_TEST_VAR", got, "some-value")
	}
}

// TestMustEnvFatalsWhenUnset verifies the missing-variable path by re-running
// this test binary in a subprocess: the child dies via log.Fatal before it
// can report a PASS, so the parent observes a non-zero exit status and the
// fatal message naming the variable.
func TestMustEnvFatalsWhenUnset(t *testing.T) {
	if os.Getenv("MUSTENV_CHILD") == "1" {
		mustEnv("MUSTENV_MISSING_VAR") //nolint:staticcheck // SA1020: process exits here by design
		return
	}

	var stderr bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestMustEnvFatalsWhenUnset$")
	cmd.Env = append(os.Environ(), "MUSTENV_CHILD=1", "MUSTENV_MISSING_VAR=")
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("mustEnv on unset variable: want non-zero process exit, got err=%v", err)
	}
	if !strings.Contains(stderr.String(), "MUSTENV_MISSING_VAR environment variable is not set") {
		t.Fatalf("fatal message = %q, want it to name %q as not set", stderr.String(), "MUSTENV_MISSING_VAR")
	}
}

// The tests below pin the service's HTTP surface, as main() wires it today:
// which routes exist, which requests they answer, and which requests the
// endpoint counters count. They build the surface through newMainMux with
// lightweight fakes, so no Firestore, price API, or Telegram is involved.

// emptyAlertsRepo is an AlertsRepository holding nothing: a check run over
// it finds no alerts and succeeds without touching any other dependency,
// which makes the /check-alerts success path testable.
type emptyAlertsRepo struct{}

func (emptyAlertsRepo) AddAlert(context.Context, alerts.PriceAlert) {}

func (emptyAlertsRepo) GetAllAlerts(context.Context) ([]alerts.PriceAlert, error) {
	return nil, nil
}

func (emptyAlertsRepo) GetAlertsByUserID(context.Context, int64) ([]alerts.PriceAlert, error) {
	return nil, nil
}

func (emptyAlertsRepo) GetAlertByID(context.Context, int64, string) (*alerts.PriceAlert, error) {
	return nil, nil
}

func (emptyAlertsRepo) DeleteAlert(context.Context, alerts.PriceAlert) error { return nil }

func (emptyAlertsRepo) MarkDeliveryFailed(context.Context, alerts.PriceAlert, time.Time) error {
	return nil
}

// failingAlertsRepo fails every listing, so a check run over it errors and
// the /check-alerts endpoint reports the failure instead of completing.
type failingAlertsRepo struct{ emptyAlertsRepo }

func (failingAlertsRepo) GetAllAlerts(context.Context) ([]alerts.PriceAlert, error) {
	return nil, errors.New("firestore unavailable")
}

// mainMuxUnderTest builds the HTTP surface against a repository of the
// caller's choosing and a Telegram handler whose bot and repository are nil:
// every request these tests make is answered by the webhook's method and
// JSON validation before any send or lookup can happen.
func mainMuxUnderTest(repo handlers.AlertsRepository) *http.ServeMux {
	checker := handlers.NewAlertChecker(repo, nil, nil)
	telegram := handlers.NewTelegramWebhookHandler(nil, nil, nil, "")
	return newMainMux(checker, telegram)
}

func TestHealthEndpointAnswersOK(t *testing.T) {
	rec := httptest.NewRecorder()
	mainMuxUnderTest(emptyAlertsRepo{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "OK" {
		t.Fatalf("GET /health body = %q, want %q", got, "OK")
	}
}

func TestWebhookEndpointRejectsNonPostRequests(t *testing.T) {
	before := testutil.ToFloat64(webhookMetric)
	rec := httptest.NewRecorder()
	mainMuxUnderTest(emptyAlertsRepo{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhook", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /webhook status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	// The endpoint counts every request it receives, rejected ones included.
	if delta := testutil.ToFloat64(webhookMetric) - before; delta != 1 {
		t.Errorf("webhookMetric delta after one request = %v, want 1", delta)
	}
}

func TestWebhookEndpointRejectsInvalidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("not json"))
	mainMuxUnderTest(emptyAlertsRepo{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /webhook with garbage status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestWebhookEndpointAcceptsUpdateWithoutMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"update_id":1}`))
	mainMuxUnderTest(emptyAlertsRepo{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /webhook update without message: status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCheckAlertsEndpointReportsCompletion(t *testing.T) {
	before := testutil.ToFloat64(checkAlertMetric)
	rec := httptest.NewRecorder()
	mainMuxUnderTest(emptyAlertsRepo{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/check-alerts", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /check-alerts status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "Alert check completed" {
		t.Fatalf("GET /check-alerts body = %q, want %q", got, "Alert check completed")
	}
	if delta := testutil.ToFloat64(checkAlertMetric) - before; delta != 1 {
		t.Errorf("checkAlertMetric delta after one request = %v, want 1", delta)
	}
}

func TestCheckAlertsEndpointReportsCheckFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	mainMuxUnderTest(failingAlertsRepo{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/check-alerts", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /check-alerts on failing storage status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := rec.Body.String(); !strings.Contains(got, "Error checking alerts") {
		t.Fatalf("GET /check-alerts failure body = %q, want it to report the check error", got)
	}
}

func TestPprofRoutesAreRegistered(t *testing.T) {
	rec := httptest.NewRecorder()
	mainMuxUnderTest(emptyAlertsRepo{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ status = %d, want %d", rec.Code, http.StatusOK)
	}
}
