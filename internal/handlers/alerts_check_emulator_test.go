package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
)

// This test drives the real alert-checking loop end to end with the REAL
// Firestore repository (against the Firestore emulator, started standalone
// (not via `go test`), e.g.:
//
//	gcloud beta emulators firestore start --host-port=127.0.0.1:8990
//	FIRESTORE_EMULATOR_HOST=127.0.0.1:8990 go test ./internal/handlers -run TestCheckAlertsWithRealFirestore -v
//
// Only the two external SaaS endpoints are replaced: Telegram by a stub HTTP
// transport under the real BotAPI client, and CoinMarketCap by an httptest
// server under the real PriceService. Alert storage, triggering, retry,
// failure stamping, and deletion all run the exact production code paths.
// Skipped unless FIRESTORE_EMULATOR_HOST is set.

// TestCheckAlertsWithRealFirestoreAtLeastOnceLoop runs the full cycle the
// scheduler drives: a triggered alert whose Telegram sends all fail is kept
// in real Firestore with a delivery_failed_at stamp; once Telegram recovers,
// the next run redelivers it and deletes it from real Firestore - and the
// alert is still in storage at the moment the send happens (send-first).
func TestCheckAlertsWithRealFirestoreAtLeastOnceLoop(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; run gcloud beta emulators firestore start to enable")
	}

	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "no-mistakes-emulator-test")
	if err != nil {
		t.Fatalf("creating firestore client for emulator: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	repo := adapters.NewAlertsFirestoreRepository(client)

	// Start from a clean alerts collection.
	docs, err := client.Collection("alerts").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("listing alerts for cleanup: %v", err)
	}
	for _, doc := range docs {
		if _, err := doc.Ref.Delete(ctx); err != nil {
			t.Fatalf("cleanup delete of %s: %v", doc.Ref.ID, err)
		}
	}

	// Fake CoinMarketCap: BTC trades at $51000, above the alert target.
	cmc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"BTC":{"quote":{"USD":{"price":51000.0}}}}}`))
	}))
	t.Cleanup(cmc.Close)

	// Real BotAPI client with the stub transport.
	transport := &stubTelegramTransport{}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, transport)
	if err != nil {
		t.Fatalf("creating bot with stub transport: %v", err)
	}

	dumpStored := func(label string) {
		t.Helper()
		stored, err := repo.GetAllAlerts(ctx)
		if err != nil {
			t.Fatalf("%s: GetAllAlerts: %v", label, err)
		}
		raw, _ := json.Marshal(stored)
		t.Logf("%s: real Firestore holds %d alert(s): %s", label, len(stored), raw)
	}

	checker := NewAlertCheckerWithClock(
		repo, services.NewPriceServiceWithEndpoint("test-cmc-key", nil, time.Minute, cmc.URL), bot,
		&fakeClock{now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)},
	)

	repo.AddAlert(ctx, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})
	dumpStored("after /setalert")

	// Send-first ordering, observed at the real storage boundary: at the
	// moment the notification reaches Telegram, the alert is still stored.
	stillStoredAtSend := false
	transport.onSend = func() {
		stored, _ := repo.GetAllAlerts(ctx)
		stillStoredAtSend = len(stored) == 1
	}

	// Scheduled run 1: Telegram is down, all 4 attempts fail.
	transport.failEverySends = true
	if err := checker.CheckAlerts(ctx); err != nil {
		t.Fatalf("CheckAlerts (failing run): %v", err)
	}
	if got, want := transport.sendAttempts(), 4; got != want {
		t.Errorf("failing run: expected %d send attempts, got %d", want, got)
	}
	stored, err := repo.GetAllAlerts(ctx)
	if err != nil || len(stored) != 1 {
		t.Fatalf("after a fully failed run the alert must be kept in real Firestore: %v, %d alert(s)", err, len(stored))
	}
	if stored[0].DeliveryFailedAt.IsZero() {
		t.Error("kept alert must carry a delivery_failed_at stamp in real Firestore")
	}
	dumpStored("after failed run")

	// Scheduled run 2: Telegram has recovered.
	transport.failEverySends = false
	transport.onSend = nil
	if err := checker.CheckAlerts(ctx); err != nil {
		t.Fatalf("CheckAlerts (recovered run): %v", err)
	}

	if !stillStoredAtSend {
		t.Error("alert must still be in real Firestore at the moment the notification is sent (send-first)")
	}
	messages := transport.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("the user must receive exactly one notification after recovery, got %d", len(messages))
	}
	if messages[0].ChatID != 42 {
		t.Errorf("notification must go to chat 42, got %d", messages[0].ChatID)
	}
	stored, err = repo.GetAllAlerts(ctx)
	if err != nil || len(stored) != 0 {
		t.Fatalf("alert must be deleted from real Firestore after successful delivery: %v, %d alert(s)", err, len(stored))
	}
	dumpStored("after successful redelivery")
}
