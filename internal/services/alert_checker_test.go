package services

import (
	"context"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// fake implementations
type fakeRepo struct {
	alerts []alerts.PriceAlert
}

func (f *fakeRepo) GetAllAlerts(ctx context.Context) ([]alerts.PriceAlert, error) {
	return f.alerts, nil
}

type fakePrice struct {
	priceMap map[string]float64
}

func (p *fakePrice) GetPrice(symbol string) (float64, error) {
	v, ok := p.priceMap[symbol]
	if !ok {
		return 0, nil
	}
	return v, nil
}

type fakeBot struct {
	sent []tgbotapi.Chattable
}

func (b *fakeBot) Send(chattable tgbotapi.Chattable) (tgbotapi.Message, error) {
	b.sent = append(b.sent, chattable)
	return tgbotapi.Message{}, nil
}

func TestAlertChecker_CheckAlerts_SendsNotification(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{alerts: []alerts.PriceAlert{
		{Symbol: "BTC", TargetPrice: 1000, UserID: 123, Type: alerts.More},
	}}
	price := &fakePrice{priceMap: map[string]float64{"BTC": 2000}}
	bot := &fakeBot{}

	ac := NewAlertChecker(repo, price, bot)
	if err := ac.CheckAlerts(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bot.sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(bot.sent))
	}
}
