package adapters

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// AlertFirestoreModel represents the data structure for storing alerts in Firestore
type AlertFirestoreModel struct {
	UserID      int64   `firestore:"user_id"`
	CoinID      string  `firestore:"coin_id"`
	TargetPrice float64 `firestore:"target_price"`
	CreatedAt   int64   `firestore:"created_at"`
	Type        string  `firestore:"type"`
	// DeliveryFailedAt is a Unix timestamp in milliseconds; zero means the
	// alert has never failed delivery. omitempty keeps documents written by
	// older code (and fresh alerts) free of the field.
	DeliveryFailedAt int64 `firestore:"delivery_failed_at,omitempty"`
}

// mapToFirestoreModel converts a domain PriceAlert to a Firestore model.
// DeliveryFailedAt is intentionally not mapped: this mapper only creates
// fresh alerts via AddAlert; failure stamps are written in place by
// MarkDeliveryFailed, never by a full-document rewrite.
func mapToFirestoreModel(alert alerts.PriceAlert) AlertFirestoreModel {
	return AlertFirestoreModel{
		UserID:      alert.UserID,
		CoinID:      alert.Symbol,
		TargetPrice: alert.TargetPrice,
		Type:        alert.Type.String(),
		CreatedAt:   time.Now().Unix(),
	}
}

// mapToDomainModel converts a Firestore model to a domain PriceAlert
func mapToDomainModel(model AlertFirestoreModel, docID string) (alerts.PriceAlert, error) {
	// Unknown type strings default to More so existing documents keep
	// loading; the error return stays dormant, matching today's behavior.
	alertType, err := alerts.ParseAlertType(model.Type)
	if err != nil {
		alertType = alerts.More
	}

	return alerts.PriceAlert{
		Id:          docID,
		UserID:      model.UserID,
		Symbol:      model.CoinID,
		TargetPrice: model.TargetPrice,
		Type:        alertType,
		// Zero stays zero: an alert that never failed delivery keeps a zero
		// DeliveryFailedAt after the round trip.
		DeliveryFailedAt: msToTime(model.DeliveryFailedAt),
	}, nil
}

// msToTime converts a Unix-milliseconds timestamp to time.Time. Zero or
// negative (unset) values map to the zero time.
func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// timeToMs converts a time.Time to a Unix-milliseconds timestamp; the zero
// time maps to zero so it round-trips through msToTime unchanged.
func timeToMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

type AlertsFirestoreRepository struct {
	firestoreClient *firestore.Client
}

func NewAlertsFirestoreRepository(firestoreClient *firestore.Client) *AlertsFirestoreRepository {
	if firestoreClient == nil {
		panic("missing firestore client")
	}

	return &AlertsFirestoreRepository{firestoreClient}
}

func (r *AlertsFirestoreRepository) alertCollection() *firestore.CollectionRef {
	return r.firestoreClient.Collection("alerts")
}

func (r *AlertsFirestoreRepository) AddAlert(ctx context.Context, alert alerts.PriceAlert) {
	collection := r.alertCollection()

	// Convert domain model to Firestore model
	firestoreModel := mapToFirestoreModel(alert)

	// Add the document to Firestore
	_, _, err := collection.Add(ctx, firestoreModel)
	if err != nil {
		// Handle error appropriately
		// Consider returning the error or logging it
	}
}

// GetAllAlerts retrieves all alerts from Firestore
func (r *AlertsFirestoreRepository) GetAllAlerts(ctx context.Context) ([]alerts.PriceAlert, error) {
	collection := r.alertCollection()
	docs, err := collection.Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}

	var result []alerts.PriceAlert
	for _, doc := range docs {
		var model AlertFirestoreModel
		if err := doc.DataTo(&model); err != nil {
			continue
		}
		domainAlert, err := mapToDomainModel(model, doc.Ref.ID)
		if err != nil {
			continue
		}
		result = append(result, domainAlert)
	}

	return result, nil
}

// GetAlertsByUserID retrieves all alerts for a specific user from Firestore
func (r *AlertsFirestoreRepository) GetAlertsByUserID(ctx context.Context, userID int64) ([]alerts.PriceAlert, error) {
	collection := r.alertCollection()
	docs, err := collection.Where("user_id", "==", userID).Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}

	var result []alerts.PriceAlert
	for _, doc := range docs {
		var model AlertFirestoreModel
		if err := doc.DataTo(&model); err != nil {
			continue
		}
		domainAlert, err := mapToDomainModel(model, doc.Ref.ID)
		if err != nil {
			continue
		}
		result = append(result, domainAlert)
	}

	return result, nil
}

func (r *AlertsFirestoreRepository) DeleteAlert(ctx context.Context, alert alerts.PriceAlert) error {
	collection := r.alertCollection()

	_, err := collection.Doc(alert.Id).Delete(ctx)
	return err
}

// MarkDeliveryFailed persists the alert's delivery-failure timestamp on the
// stored document so the next scheduled check can pick it up for redelivery
// (and, later, dead-letter it once it has failed long enough). It updates
// only the delivery_failed_at field and returns NotFound when the document
// no longer exists (e.g. deleted concurrently): a deleted alert is never
// resurrected.
func (r *AlertsFirestoreRepository) MarkDeliveryFailed(ctx context.Context, alert alerts.PriceAlert, failedAt time.Time) error {
	_, err := r.alertCollection().Doc(alert.Id).Update(ctx, []firestore.Update{
		{Path: "delivery_failed_at", Value: timeToMs(failedAt)},
	})
	return err
}
