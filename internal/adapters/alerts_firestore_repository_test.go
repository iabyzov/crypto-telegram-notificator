package adapters

import (
	"testing"
	"time"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

func TestDeliveryFailedAtRoundTrip(t *testing.T) {
	failedAt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	// A failure-stamped alert loaded from its Firestore model keeps the stamp.
	model := AlertFirestoreModel{
		UserID: 42, CoinID: "BTC", TargetPrice: 50000, Type: "More",
		DeliveryFailedAt: timeToMs(failedAt),
	}
	loaded := mapToDomainModel(model, "doc-1")
	if !loaded.DeliveryFailedAt.Equal(failedAt) {
		t.Errorf("loaded stamp = %v, want %v", loaded.DeliveryFailedAt, failedAt)
	}

	// A document written before the field existed (absent) loads as never
	// failed: zero maps to the zero time.
	legacy := mapToDomainModel(AlertFirestoreModel{UserID: 42, CoinID: "BTC", TargetPrice: 1, Type: "Less"}, "doc-2")
	if !legacy.DeliveryFailedAt.IsZero() {
		t.Errorf("missing delivery_failed_at must load as zero time, got %v", legacy.DeliveryFailedAt)
	}

	// mapToFirestoreModel never writes a stamp: fresh alerts always start
	// undelivered, regardless of the domain alert's stamp.
	fresh := mapToFirestoreModel(alerts.PriceAlert{
		Symbol: "BTC", TargetPrice: 50000, UserID: 42, Type: alerts.More,
		DeliveryFailedAt: failedAt,
	})
	if fresh.DeliveryFailedAt != 0 {
		t.Errorf("fresh alerts must not carry a delivery failure stamp, got %d", fresh.DeliveryFailedAt)
	}
}
