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

// mapToDomainModel converts a Firestore model to a domain PriceAlert
func mapToDomainModel(model AlertFirestoreModel) (alerts.PriceAlert, error) {
	var alertType alerts.AlertType
	switch model.Type {
	case "More":
		alertType = alerts.More
	case "Less":
		alertType = alerts.Less
	default:
		alertType = alerts.More
	}

	return alerts.PriceAlert{
		UserID:      model.UserID,
		Symbol:      model.CoinID,
		TargetPrice: model.TargetPrice,
		Type:        alertType,
	}, nil
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

// GetAllAlerts retrieves all alerts from Firestore
func (r AlertsFirestoreRepository) GetAllAlerts(ctx context.Context) ([]alerts.PriceAlert, error) {
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
		domainAlert, err := mapToDomainModel(model)
		if err != nil {
			continue
		}
		result = append(result, domainAlert)
	}

	return result, nil
}
