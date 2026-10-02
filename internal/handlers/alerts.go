package handlers

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

// telegramNotificationErrors counts every failed Telegram notification
// attempt and every terminal dead-lettering, so Cloud Monitoring can alarm
// on delivery failures (ticket #33).
var telegramNotificationErrors = promauto.NewCounter(prometheus.CounterOpts{
	Name: "telegram_notification_errors_total",
	Help: "Total failed Telegram notification attempts and dead-lettered alerts",
})

// checkerClock is the checker's view of time: when delivery failures are
// stamped, and how the retry backoff waits. Injectable so tests make the
// 2s/4s/8s schedule deterministic.
type checkerClock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// realClock is checkerClock backed by the wall clock.
type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// notificationRetryDelays is the exponential backoff between notification
// attempts: the first attempt is immediate, then waits of 2s, 4s, and 8s
// precede the second, third, and fourth attempts. Four attempts total, per
// ADR-0001's at-least-once delivery contract.
var notificationRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// AlertChecker handles checking alerts and sending notifications
type AlertChecker struct {
	alertsRepository AlertsRepository
	priceService     *services.PriceService
	bot              *tgbotapi.BotAPI
	clock            checkerClock
}

// NewAlertChecker creates a new AlertChecker
func NewAlertChecker(
	alertsRepository AlertsRepository,
	priceService *services.PriceService,
	bot *tgbotapi.BotAPI,
) *AlertChecker {
	return NewAlertCheckerWithClock(alertsRepository, priceService, bot, realClock{})
}

// NewAlertCheckerWithClock is NewAlertChecker with an injected clock, the seam
// tests use to make retry backoff deterministic.
func NewAlertCheckerWithClock(
	alertsRepository AlertsRepository,
	priceService *services.PriceService,
	bot *tgbotapi.BotAPI,
	clock checkerClock,
) *AlertChecker {
	return &AlertChecker{
		alertsRepository: alertsRepository,
		priceService:     priceService,
		bot:              bot,
		clock:            clock,
	}
}

// CheckAlerts fetches all alerts, checks them against current prices, and sends
// notifications. Delivery is at-least-once: each triggered alert's
// notification is sent first and the alert is deleted only after a successful
// send; a notification that fails all retry attempts keeps its alert in
// storage, stamped with delivery_failed_at, for the next scheduled run.
// Before any pricing or delivery, alerts whose delivery has been failing for
// more than an hour are terminally dead-lettered (issue #29): deleted,
// counted, and logged - the loss is observable instead of silent.
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

	// Dead-letter sweep: alerts failing delivery for over an hour are
	// terminal. Keyed on age alone, whether or not they still trigger; the
	// sweep runs before pricing so dead alerts never spend CoinMarketCap
	// quota.
	liveAlerts := make([]alerts.PriceAlert, 0, len(allAlerts))
	for _, alert := range allAlerts {
		if ac.deadLetterIfExpired(ctx, alert) {
			continue
		}
		liveAlerts = append(liveAlerts, alert)
	}

	if len(liveAlerts) == 0 {
		return nil
	}

	// Group alerts by symbol to minimize API calls
	alertsBySymbol := make(map[string][]alerts.PriceAlert)
	for _, alert := range liveAlerts {
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
			if alert.IsTriggeredBy(prices[alert.Symbol]) {
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
				ac.deliverAlert(ctx, alert, prices[alert.Symbol])
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

// deliveryFailureDeadline is how long an alert may keep failing delivery
// before it is terminally dead-lettered (ADR-0001, issue #29). Counted from
// the FIRST failure: later failures keep the earliest stamp.
const deliveryFailureDeadline = time.Hour

// deadLetterIfExpired terminally dead-letters one alert when its delivery
// has been failing for over the deadline: delete from storage, increment
// telegram_notification_errors_total, and emit an slog ERROR with the
// alert's details so the loss is observable. It reports whether the alert
// was dead-lettered (and must be skipped by the rest of the run).
func (ac *AlertChecker) deadLetterIfExpired(ctx context.Context, alert alerts.PriceAlert) bool {
	if alert.DeliveryFailedAt.IsZero() {
		return false
	}
	if ac.clock.Now().Sub(alert.DeliveryFailedAt) <= deliveryFailureDeadline {
		return false
	}

	if err := ac.alertsRepository.DeleteAlert(ctx, alert); err != nil {
		// Not deleted: keep it in the run so a later sweep can retry the
		// dead-lettering; storage state is unchanged.
		log.Printf("Failed to delete expired alert %s during dead-lettering: %v", alert.Id, err)
		return false
	}
	telegramNotificationErrors.Inc()
	slog.Error("alert dead-lettered: notification delivery failed for over an hour",
		"alert_id", alert.Id,
		"user_id", alert.UserID,
		"symbol", alert.Symbol,
		"target_price", alert.TargetPrice,
		"alert_type", alert.Type.String(),
		"first_failed_at", alert.DeliveryFailedAt.Format(time.RFC3339),
	)
	return true
}

// deliverAlert delivers one triggered alert at-least-once: send the
// notification with retry backoff, delete the alert only after a successful
// send, and on total failure stamp delivery_failed_at and keep the alert for
// the next scheduled run. The stamp records the FIRST failure: an alert
// already carrying a stamp keeps its earliest one, so the dead-letter
// deadline measures from the first failure.
func (ac *AlertChecker) deliverAlert(ctx context.Context, alert alerts.PriceAlert, currentPrice float64) {
	if err := ac.sendNotificationWithRetry(ctx, alert, currentPrice); err != nil {
		log.Printf("Notification delivery failed for alert %s (user %d, %s): %v",
			alert.Id, alert.UserID, alert.Symbol, err)
		if alert.DeliveryFailedAt.IsZero() {
			if markErr := ac.alertsRepository.MarkDeliveryFailed(ctx, alert, ac.clock.Now()); markErr != nil {
				log.Printf("Failed to mark alert %s as delivery-failed: %v", alert.Id, markErr)
			}
		}
		return
	}
	if err := ac.alertsRepository.DeleteAlert(ctx, alert); err != nil {
		log.Printf("Failed to delete alert %s after successful delivery: %v", alert.Id, err)
	}
}

// sendNotificationWithRetry attempts the notification up to len(delays)+1
// times, sleeping delays[i] before attempt i+1. It returns nil as soon as one
// attempt succeeds, the context error if the run is cancelled mid-backoff, or
// the last error after all attempts fail.
func (ac *AlertChecker) sendNotificationWithRetry(ctx context.Context, alert alerts.PriceAlert, currentPrice float64) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = ac.sendNotification(alert, currentPrice); err == nil {
			return nil
		}
		// Every failed attempt is an operator-visible notification error
		// (ticket #33), not just the terminal give-up.
		telegramNotificationErrors.Inc()
		if attempt >= len(notificationRetryDelays) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ac.clock.Sleep(notificationRetryDelays[attempt])
	}
}

// sendNotification sends a Telegram notification to the user
func (ac *AlertChecker) sendNotification(alert alerts.PriceAlert, currentPrice float64) error {
	presentation := alertTypePresentationFor(alert.Type)
	message := fmt.Sprintf(
		"%s Alert triggered for %s!\nCurrent price: $%.2f\nTarget price: $%.2f (%s)\nThe price has %s!",
		presentation.emoji,
		alert.Symbol,
		currentPrice,
		alert.TargetPrice,
		alert.Type,
		presentation.triggerPhrase,
	)

	msg := tgbotapi.NewMessage(alert.UserID, message)
	if _, err := ac.bot.Send(msg); err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}

	log.Printf("Alert notification sent to user %d for %s at $%.2f", alert.UserID, alert.Symbol, currentPrice)
	return nil
}
