package handlers

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var checkAlertDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "price_check_duration_seconds",
	Help:    "End-to-end duration of CheckAlerts() runs in seconds",
	Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
})

// AlertChecker handles checking alerts and sending notifications
type AlertChecker struct {
	alertsRepository *adapters.AlertsFirestoreRepository
	priceService     *services.PriceService
	bot              *tgbotapi.BotAPI
}

// NewAlertChecker creates a new AlertChecker
func NewAlertChecker(
	alertsRepository *adapters.AlertsFirestoreRepository,
	priceService *services.PriceService,
	bot *tgbotapi.BotAPI,
) *AlertChecker {
	return &AlertChecker{
		alertsRepository: alertsRepository,
		priceService:     priceService,
		bot:              bot,
	}
}

// CheckAlerts fetches all alerts, checks them against current prices, and sends notifications
func (ac *AlertChecker) CheckAlerts(ctx context.Context) error {
	start := time.Now()
	defer func() {
		checkAlertDuration.Observe(time.Since(start).Seconds())
	}()

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

	var symbols []string
	for symbol := range alertsBySymbol {
		symbols = append(symbols, symbol)
	}

	prices, err := ac.priceService.GetPrices(symbols)
	if err != nil {
		return fmt.Errorf("failed to get prices: %w", err)
	}

	triggeredAlerts := []alerts.PriceAlert{}

	for _, symbolAlerts := range alertsBySymbol {
		// Check each alert for this symbol
		for _, alert := range symbolAlerts {
			if ac.isAlertTriggered(alert, prices[alert.Symbol]) {
				triggeredAlerts = append(triggeredAlerts, alert)
			}
		}
	}

	var wg sync.WaitGroup
	ch := make(chan alerts.PriceAlert, len(triggeredAlerts))

	const maxWorkers = 5
	for i := 0; i < min(maxWorkers, len(triggeredAlerts)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for alert := range ch {
				if err := ac.alertsRepository.DeleteAlert(ctx, alert); err != nil {
					log.Printf("Failed to delete alert: %v", err)
				}
				if err := ac.sendNotification(alert, prices[alert.Symbol]); err != nil {
					log.Printf("Failed to send notification for alert: %v", err)
				}
			}
		}()
	}

	for _, alert := range triggeredAlerts {
		ch <- alert
	}

	close(ch)
	wg.Wait()

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
