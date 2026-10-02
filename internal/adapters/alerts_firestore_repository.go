package adapters

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// mapToDomainModel converts a Firestore model to a domain PriceAlert.
// An unparseable type string defaults to More so documents written by
// older code keep loading.
func mapToDomainModel(model AlertFirestoreModel, docID string) alerts.PriceAlert {
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
	}
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

// AddAlert stores the alert as a new document. The write's error is
// deliberately swallowed: AlertsRepository gives AddAlert no error return,
// so a failed write is silent — nothing is stored, and the bot still sends
// the user its confirmation message.
func (r *AlertsFirestoreRepository) AddAlert(ctx context.Context, alert alerts.PriceAlert) {
	_, _, _ = r.alertCollection().Add(ctx, mapToFirestoreModel(alert))
}

// GetAllAlerts retrieves all alerts from Firestore
func (r *AlertsFirestoreRepository) GetAllAlerts(ctx context.Context) ([]alerts.PriceAlert, error) {
	return alertsFrom(ctx, r.alertCollection().Documents(ctx))
}

// GetAlertsByUserID retrieves all alerts for a specific user from Firestore
func (r *AlertsFirestoreRepository) GetAlertsByUserID(ctx context.Context, userID int64) ([]alerts.PriceAlert, error) {
	return alertsFrom(ctx, r.alertCollection().Where("user_id", "==", userID).Documents(ctx))
}

// GetAlertByID loads the one alert document addressed by alertID with a
// single direct read and returns it only when it belongs to userID. It
// answers nil (not an error) whenever the id cannot name the user's stored
// alert: no such document, a document name Firestore rejects, a document
// that fails to decode, or one owned by a different user — the same "not
// found" a scan of the user's alerts would report for that id.
func (r *AlertsFirestoreRepository) GetAlertByID(ctx context.Context, userID int64, alertID string) (*alerts.PriceAlert, error) {
	snapshot, err := r.alertCollection().Doc(alertID).Get(ctx)
	if err != nil {
		// A document name Firestore refuses (containing "/", being "." or
		// "..", …) can never name a stored alert, so both "no such
		// document" and "malformed document name" are simply not found.
		if status.Code(err) == codes.NotFound || status.Code(err) == codes.InvalidArgument {
			return nil, nil
		}
		return nil, err
	}

	var model AlertFirestoreModel
	if err := snapshot.DataTo(&model); err != nil {
		return nil, nil
	}
	if model.UserID != userID {
		return nil, nil
	}

	alert := mapToDomainModel(model, snapshot.Ref.ID)
	return &alert, nil
}

// alertsFrom drains a Firestore query into domain alerts. Documents that
// fail to decode (or whose type string is unparseable) are skipped, not
// failed: one bad document must not block the rest of the load.
func alertsFrom(ctx context.Context, iter *firestore.DocumentIterator) ([]alerts.PriceAlert, error) {
	docs, err := iter.GetAll()
	if err != nil {
		return nil, err
	}

	var result []alerts.PriceAlert
	for _, doc := range docs {
		var model AlertFirestoreModel
		if err := doc.DataTo(&model); err != nil {
			continue
		}
		result = append(result, mapToDomainModel(model, doc.Ref.ID))
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
