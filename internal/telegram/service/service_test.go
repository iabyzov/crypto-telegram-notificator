package service

import (
	"context"
	"testing"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// fakeTelegramRepository implements AlertsRepository interface for testing
type fakeTelegramRepository struct {
	alerts        []alerts.PriceAlert
	deletedAlerts []string
}

func (f *fakeTelegramRepository) AddAlert(ctx context.Context, alert alerts.PriceAlert) {
	f.alerts = append(f.alerts, alert)
}

func (f *fakeTelegramRepository) GetAlertsByUserID(ctx context.Context, userID int64) ([]alerts.PriceAlert, error) {
	var result []alerts.PriceAlert
	for _, alert := range f.alerts {
		if alert.UserID == userID {
			result = append(result, alert)
		}
	}
	return result, nil
}

func (f *fakeTelegramRepository) DeleteAlert(ctx context.Context, alert alerts.PriceAlert) error {
	f.deletedAlerts = append(f.deletedAlerts, alert.Id)
	// Remove from alerts slice
	for i, a := range f.alerts {
		if a.Id == alert.Id {
			f.alerts = append(f.alerts[:i], f.alerts[i+1:]...)
			break
		}
	}
	return nil
}

func TestGetAlertsByUserID_FiltersCorrectly(t *testing.T) {
	repo := &fakeTelegramRepository{
		alerts: []alerts.PriceAlert{
			{Id: "alert1", Symbol: "BTC", TargetPrice: 50000, UserID: 123, Type: alerts.More},
			{Id: "alert2", Symbol: "ETH", TargetPrice: 2000, UserID: 456, Type: alerts.Less},
			{Id: "alert3", Symbol: "BTC", TargetPrice: 40000, UserID: 123, Type: alerts.Less},
		},
	}

	ctx := context.Background()
	userAlerts, err := repo.GetAlertsByUserID(ctx, 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(userAlerts) != 2 {
		t.Fatalf("expected 2 alerts for user 123, got %d", len(userAlerts))
	}

	for _, alert := range userAlerts {
		if alert.UserID != 123 {
			t.Errorf("expected alert for user 123, got user %d", alert.UserID)
		}
	}
}

func TestDeleteAlert_RemovesAlert(t *testing.T) {
	repo := &fakeTelegramRepository{
		alerts: []alerts.PriceAlert{
			{Id: "alert1", Symbol: "BTC", TargetPrice: 50000, UserID: 123, Type: alerts.More},
			{Id: "alert2", Symbol: "ETH", TargetPrice: 2000, UserID: 123, Type: alerts.Less},
		},
		deletedAlerts: []string{},
	}

	ctx := context.Background()
	alertToDelete := repo.alerts[0]
	err := repo.DeleteAlert(ctx, alertToDelete)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repo.deletedAlerts) != 1 {
		t.Fatalf("expected 1 deleted alert, got %d", len(repo.deletedAlerts))
	}
	if repo.deletedAlerts[0] != "alert1" {
		t.Errorf("expected deleted alert 'alert1', got %s", repo.deletedAlerts[0])
	}

	if len(repo.alerts) != 1 {
		t.Fatalf("expected 1 remaining alert, got %d", len(repo.alerts))
	}
	if repo.alerts[0].Id != "alert2" {
		t.Errorf("expected remaining alert 'alert2', got %s", repo.alerts[0].Id)
	}
}

func TestGetAlertsByUserID_OnlyReturnsUserAlerts(t *testing.T) {
	repo := &fakeTelegramRepository{
		alerts: []alerts.PriceAlert{
			{Id: "alert1", Symbol: "BTC", TargetPrice: 50000, UserID: 123, Type: alerts.More},
			{Id: "alert2", Symbol: "ETH", TargetPrice: 2000, UserID: 456, Type: alerts.Less},
		},
		deletedAlerts: []string{},
	}

	ctx := context.Background()
	
	// User 123 tries to get their alerts
	userAlerts, err := repo.GetAlertsByUserID(ctx, 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that alert2 (belonging to user 456) is not in the list
	for _, alert := range userAlerts {
		if alert.Id == "alert2" {
			t.Error("user 123 should not see alert2 belonging to user 456")
		}
	}

	if len(userAlerts) != 1 {
		t.Fatalf("expected 1 alert for user 123, got %d", len(userAlerts))
	}
}
