package function

import (
	"context"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/alertjob"
)

// CheckPriceAlerts is the Cloud Function entry point for scheduled alert checking
func CheckPriceAlerts(ctx context.Context, m interface{}) error {
	return alertjob.CheckPriceAlerts(ctx, m)
}
