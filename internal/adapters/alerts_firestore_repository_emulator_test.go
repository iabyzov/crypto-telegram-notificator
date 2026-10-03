package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// These tests drive the real AlertsFirestoreRepository against a real
// Firestore engine, started standalone (not via `go test`), e.g.:
//
//	gcloud beta emulators firestore start --host-port=127.0.0.1:8990
//	FIRESTORE_EMULATOR_HOST=127.0.0.1:8990 go test ./internal/adapters -run TestFirestore -v
//
// They are skipped unless FIRESTORE_EMULATOR_HOST is set, so environments
// without an emulator are unaffected. The in-memory fakes used by the
// handler tests cannot pin Firestore-specific behavior (e.g. that a field
// Update on a deleted document does not recreate it), which is exactly what
// these tests pin.

// newEmulatorRepo connects to the Firestore emulator and returns the real
// repository backed by it, plus a cleanup func that wipes the alerts
// collection.
func newEmulatorRepo(t *testing.T) (*AlertsFirestoreRepository, *firestore.Client) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; run gcloud beta emulators firestore start to enable")
	}

	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "no-mistakes-emulator-test")
	if err != nil {
		t.Fatalf("creating firestore client for emulator: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	repo := NewAlertsFirestoreRepository(client)

	// Wipe the alerts collection so each test starts from a known state.
	docs, err := client.Collection("alerts").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("listing alerts for cleanup: %v", err)
	}
	for _, doc := range docs {
		if _, err := doc.Ref.Delete(ctx); err != nil {
			t.Fatalf("cleanup delete of %s: %v", doc.Ref.ID, err)
		}
	}
	return repo, client
}

// TestFirestoreLegacyDocumentWithoutDeliveryFailedAtLoads pins the round trip
// against the real engine: documents written before the field existed keep
// loading as never-failed alerts.
func TestFirestoreLegacyDocumentWithoutDeliveryFailedAtLoads(t *testing.T) {
	repo, _ := newEmulatorRepo(t)
	ctx := context.Background()

	_, err := repo.alertCollection().Doc("legacy-1").Create(ctx, map[string]any{
		"user_id":      int64(42),
		"coin_id":      "BTC",
		"target_price": 50000.0,
		"created_at":   time.Now().Unix(),
		"type":         "More",
		// no delivery_failed_at: written by code older than this change
	})
	if err != nil {
		t.Fatalf("creating legacy document: %v", err)
	}

	loaded, err := repo.GetAllAlerts(ctx)
	if err != nil {
		t.Fatalf("GetAllAlerts: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("legacy document must load, got %d alerts", len(loaded))
	}
	got := loaded[0]
	if got.Id != "legacy-1" || got.UserID != 42 || got.Symbol != "BTC" ||
		got.TargetPrice != 50000 || got.Type != alerts.More {
		t.Fatalf("legacy document fields wrong: %+v", got)
	}
	if !got.DeliveryFailedAt.IsZero() {
		t.Errorf("legacy document without delivery_failed_at must load as never failed, got %v", got.DeliveryFailedAt)
	}
}

// TestFirestoreMarkDeliveryFailedStampsInPlace pins the write against the
// real engine: AddAlert stores a fresh alert with no stamp, MarkDeliveryFailed
// writes delivery_failed_at without touching any other field, and the stamp
// survives the load round trip.
func TestFirestoreMarkDeliveryFailedStampsInPlace(t *testing.T) {
	repo, client := newEmulatorRepo(t)
	ctx := context.Background()

	repo.AddAlert(ctx, alerts.PriceAlert{
		Symbol: "ETH", TargetPrice: 3000, UserID: 7, Type: alerts.Less,
	})

	stored, err := repo.GetAlertsByUserID(ctx, 7)
	if err != nil || len(stored) != 1 {
		t.Fatalf("GetAlertsByUserID after AddAlert: %v, %d alerts", err, len(stored))
	}
	fresh := stored[0]

	// A fresh alert carries no stamp in the document.
	snapshot, err := client.Collection("alerts").Doc(fresh.Id).Get(ctx)
	if err != nil {
		t.Fatalf("reading fresh document: %v", err)
	}
	if _, ok := snapshot.Data()["delivery_failed_at"]; ok {
		t.Errorf("fresh alert must be stored without delivery_failed_at, document = %v", snapshot.Data())
	}

	failedAt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := repo.MarkDeliveryFailed(ctx, fresh, failedAt); err != nil {
		t.Fatalf("MarkDeliveryFailed: %v", err)
	}

	// The stamp is in the document, and the other fields are untouched.
	snapshot, err = client.Collection("alerts").Doc(fresh.Id).Get(ctx)
	if err != nil {
		t.Fatalf("reading stamped document: %v", err)
	}
	data := snapshot.Data()
	if got := data["delivery_failed_at"]; got != int64(failedAt.UnixMilli()) {
		t.Errorf("delivery_failed_at = %v, want %v", got, failedAt.UnixMilli())
	}
	if got := data["user_id"]; got != int64(7) {
		t.Errorf("user_id must be untouched by the stamp, got %v", got)
	}
	if got := data["coin_id"]; got != "ETH" {
		t.Errorf("coin_id must be untouched by the stamp, got %v", got)
	}
	if got := data["target_price"]; got != 3000.0 {
		t.Errorf("target_price must be untouched by the stamp, got %v", got)
	}
	if got := data["type"]; got != "Less" {
		t.Errorf("type must be untouched by the stamp, got %v", got)
	}

	// The stamp survives the load round trip.
	stored, err = repo.GetAlertsByUserID(ctx, 7)
	if err != nil || len(stored) != 1 {
		t.Fatalf("GetAlertsByUserID after MarkDeliveryFailed: %v, %d alerts", err, len(stored))
	}
	if !stored[0].DeliveryFailedAt.Equal(failedAt) {
		t.Errorf("loaded stamp = %v, want %v", stored[0].DeliveryFailedAt, failedAt)
	}
}

// TestFirestoreGetAlertByIDResolvesOnlyTheOwnersDocument pins the direct
// by-id lookup against the real engine: the owner's read resolves exactly
// the addressed document, another user's read of the same id is not found,
// and an unknown id is not found — each without an error.
func TestFirestoreGetAlertByIDResolvesOnlyTheOwnersDocument(t *testing.T) {
	repo, _ := newEmulatorRepo(t)
	ctx := context.Background()

	repo.AddAlert(ctx, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})
	stored, err := repo.GetAlertsByUserID(ctx, 42)
	if err != nil || len(stored) != 1 {
		t.Fatalf("GetAlertsByUserID after AddAlert: %v, %d alerts", err, len(stored))
	}
	alert := stored[0]

	found, err := repo.GetAlertByID(ctx, 42, alert.Id)
	if err != nil {
		t.Fatalf("GetAlertByID for the owner: %v", err)
	}
	if found == nil || found.Id != alert.Id || found.UserID != 42 ||
		found.Symbol != "BTC" || found.TargetPrice != 50000 || found.Type != alerts.More {
		t.Fatalf("the owner's id must resolve to their alert, got %+v", found)
	}

	foreign, err := repo.GetAlertByID(ctx, 7, alert.Id)
	if err != nil {
		t.Fatalf("GetAlertByID for a foreign user: %v", err)
	}
	if foreign != nil {
		t.Errorf("another user's alert must be invisible, got %+v", foreign)
	}

	missing, err := repo.GetAlertByID(ctx, 42, "no-such-document")
	if err != nil || missing != nil {
		t.Errorf("unknown id must answer nil without error, got (%+v, %v)", missing, err)
	}
}

// TestFirestoreMarkDeliveryFailedDoesNotResurrectDeletedAlert pins the
// failure mode fixed in review: the checker stamps a delivery failure while
// the alert is concurrently deleted (user /deletealert, or an overlapping
// scheduled run). A Firestore field Update must return NotFound and leave no
// document behind; a creating write (Set with MergeAll) would resurrect the
// alert as a zombie with only delivery_failed_at, which every later run
// would try to deliver forever.
func TestFirestoreMarkDeliveryFailedDoesNotResurrectDeletedAlert(t *testing.T) {
	repo, client := newEmulatorRepo(t)
	ctx := context.Background()

	repo.AddAlert(ctx, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})
	stored, err := repo.GetAlertsByUserID(ctx, 42)
	if err != nil || len(stored) != 1 {
		t.Fatalf("GetAlertsByUserID after AddAlert: %v, %d alerts", err, len(stored))
	}
	alert := stored[0]

	// The alert is deleted concurrently while the checker still holds it.
	if err := repo.DeleteAlert(ctx, alert); err != nil {
		t.Fatalf("DeleteAlert: %v", err)
	}

	markErr := repo.MarkDeliveryFailed(ctx, alert, time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC))
	if markErr == nil {
		t.Fatal("MarkDeliveryFailed on a deleted alert must return an error, got nil (document resurrected?)")
	}
	if status.Code(markErr) != codes.NotFound {
		t.Errorf("MarkDeliveryFailed on a deleted alert should fail NotFound, got %v (code %s)", markErr, status.Code(markErr))
	}

	// The deleted alert stays deleted: no zombie document, no phantom alert
	// on the next scheduled run.
	snapshot, err := client.Collection("alerts").Doc(alert.Id).Get(ctx)
	if status.Code(err) != codes.NotFound {
		t.Errorf("document must not exist after the failed mark, Get = %v, err = %v", snapshot, err)
	}
	remaining, err := repo.GetAllAlerts(ctx)
	if err != nil {
		t.Fatalf("GetAllAlerts after failed mark: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("deleted alert must not reappear for the next scheduled run, got %d alerts: %+v", len(remaining), remaining)
	}
}

// TestFirestoreMarkDeliveryFailedOverwritesPriorStamp pins that re-stamping a
// kept alert updates the field in place rather than failing on the existing
// value (and that the rest of the document still survives it).
func TestFirestoreMarkDeliveryFailedOverwritesPriorStamp(t *testing.T) {
	repo, client := newEmulatorRepo(t)
	ctx := context.Background()

	repo.AddAlert(ctx, alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
	})
	stored, err := repo.GetAlertsByUserID(ctx, 42)
	if err != nil || len(stored) != 1 {
		t.Fatalf("GetAlertsByUserID after AddAlert: %v, %d alerts", err, len(stored))
	}
	alert := stored[0]

	first := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	second := first.Add(10 * time.Minute)
	if err := repo.MarkDeliveryFailed(ctx, alert, first); err != nil {
		t.Fatalf("first MarkDeliveryFailed: %v", err)
	}
	if err := repo.MarkDeliveryFailed(ctx, alert, second); err != nil {
		t.Fatalf("second MarkDeliveryFailed: %v", err)
	}

	snapshot, err := client.Collection("alerts").Doc(alert.Id).Get(ctx)
	if err != nil {
		t.Fatalf("reading re-stamped document: %v", err)
	}
	if got := snapshot.Data()["delivery_failed_at"]; got != int64(second.UnixMilli()) {
		t.Errorf("delivery_failed_at = %v, want %v (second stamp)", got, second.UnixMilli())
	}
}
