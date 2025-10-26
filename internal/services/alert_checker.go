package services

import (
	"context"
	"fmt"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// AlertChecker handles checking alerts and sending notifications
type AlertChecker struct {
	alertsRepository AlertsRepository
	priceService     PriceFetcher
	bot              BotSender
}

// NewAlertChecker creates a new AlertChecker
func NewAlertChecker(
	alertsRepository AlertsRepository,
	priceService PriceFetcher,
	bot BotSender,
) *AlertChecker {
	return &AlertChecker{
		alertsRepository: alertsRepository,
		priceService:     priceService,
		bot:              bot,
	}
}

// CheckAlerts fetches all alerts, checks them against current prices, and sends notifications
func (ac *AlertChecker) CheckAlerts(ctx context.Context) error {
	allAlerts, err := ac.alertsRepository.GetAllAlerts(ctx)
	if err != nil {
		return fmt.Errorf("failed to get alerts: %w", err)
	}

	if len(allAlerts) == 0 {
		log.Println("No alerts to check")
		return nil
	}

	// Group alerts by symbol to minimize API calls
	alertsBySymbol := make(map[string][]alerts.PriceAlert)
	for _, alert := range allAlerts {
		alertsBySymbol[alert.Symbol] = append(alertsBySymbol[alert.Symbol], alert)
	}

	// Check each symbol's alerts
	for symbol, symbolAlerts := range alertsBySymbol {
		currentPrice, err := ac.priceService.GetPrice(symbol)
		if err != nil {
			log.Printf("Failed to get price for %s: %v", symbol, err)
			continue
		}

		// Check each alert for this symbol
		for _, alert := range symbolAlerts {
			if ac.isAlertTriggered(alert, currentPrice) {
				if err := ac.alertsRepository.DeleteAlert(ctx, alert); err != nil {
					log.Printf("Failed to delete alert: %v", err)
				}
				if err := ac.sendNotification(alert, currentPrice); err != nil {
					log.Printf("Failed to send notification for alert: %v", err)
				}
			}
		}
	}

	return nil
}

// isAlertTriggered checks if an alert condition is met
func (ac *AlertChecker) isAlertTriggered(alert alerts.PriceAlert, currentPrice float64) bool {
	switch alert.Type {
	case alerts.More:
		return currentPrice >= alert.TargetPrice
	case alerts.Less:
		return currentPrice <= alert.TargetPrice
	default:
		return false
	}
}

// sendNotification sends a Telegram notification to the user
func (ac *AlertChecker) sendNotification(alert alerts.PriceAlert, currentPrice float64) error {
	var message string
	switch alert.Type {
	case alerts.More:
		message = fmt.Sprintf(
			"🚀 Alert triggered for %s!\nCurrent price: $%.2f\nTarget price: $%.2f (above)\nThe price has reached or exceeded your target!",
			alert.Symbol,
			currentPrice,
			alert.TargetPrice,
		)
	case alerts.Less:
		message = fmt.Sprintf(
			"📉 Alert triggered for %s!\nCurrent price: $%.2f\nTarget price: $%.2f (below)\nThe price has reached or dropped below your target!",
			alert.Symbol,
			currentPrice,
			alert.TargetPrice,
		)
	}

	msg := tgbotapi.NewMessage(alert.UserID, message)
	if _, err := ac.bot.Send(msg); err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}

	log.Printf("Alert notification sent to user %d for %s at $%.2f", alert.UserID, alert.Symbol, currentPrice)
	return nil
}
