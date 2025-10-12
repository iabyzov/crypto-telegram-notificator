package services

import (
	"context"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// AlertsRepository defines the minimal storage operations AlertChecker needs.
type AlertsRepository interface {
	GetAllAlerts(ctx context.Context) ([]alerts.PriceAlert, error)
}

// PriceFetcher provides current prices for symbols.
type PriceFetcher interface {
	GetPrice(symbol string) (float64, error)
}

// BotSender abstracts sending to Telegram so it can be faked in tests.
type BotSender interface {
	Send(chattable tgbotapi.Chattable) (tgbotapi.Message, error)
}
