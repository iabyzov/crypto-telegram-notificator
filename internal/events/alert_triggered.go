package events

import (
	"time"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

type AlertTriggeredEvent struct {
	AlertId      string           `json:"alert_id"`
	Symbol       string           `json:"symbol"`
	TargetPrice  float64          `json:"target_price"`
	CurrentPrice float64          `json:"current_price"`
	UserId       int64            `json:"user_id"`
	AlertType    alerts.AlertType `json:"alert_type"`
	TriggeredAt  time.Time        `json:"triggered_at"`
}
