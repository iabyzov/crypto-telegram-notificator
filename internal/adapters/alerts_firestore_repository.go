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
}

// mapToFirestoreModel converts a domain PriceAlert to a Firestore model
func mapToFirestoreModel(alert alerts.PriceAlert) AlertFirestoreModel {
	return AlertFirestoreModel{
		UserID:      alert.UserID,
		CoinID:      alert.Symbol,
		TargetPrice: alert.TargetPrice,
		Type:        alert.Type.String(),
		CreatedAt:   time.Now().Unix(),
	}
}

type AlertsFirestoreRepository struct {
	firestoreClient *firestore.Client
}

func NewAlertsFirestoreRepository(firestoreClient *firestore.Client) AlertsFirestoreRepository {
	if firestoreClient == nil {
		panic("missing firestore client")
	}

	return AlertsFirestoreRepository{firestoreClient}
}

func (r AlertsFirestoreRepository) alertCollection() *firestore.CollectionRef {
	return r.firestoreClient.Collection("alerts")
}

func (r AlertsFirestoreRepository) AddAlert(ctx context.Context, alert alerts.PriceAlert) {
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
