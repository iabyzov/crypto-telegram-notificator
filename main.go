package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"cloud.google.com/go/firestore"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/handlers"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	checkAlertMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "check_alert_requests_total",
		Help: "Total number of /check-alerts endpoint invocations",
	})
	webhookMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "webhook_requests_total",
		Help: "Total number of /webhook endpoint invocations",
	})
)

func main() {
	prometheus.MustRegister(checkAlertMetric, webhookMetric)
	// Get environment variables
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is not set")
	}

	cmcAPIKey := os.Getenv("CMC_API_KEY")
	if cmcAPIKey == "" {
		log.Fatal("CMC_API_KEY environment variable is not set")
	}

	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		log.Fatal("GCP_PROJECT_ID environment variable is not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Initialize Firestore client
	ctx := context.Background()
	firestoreClient, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}
	defer firestoreClient.Close()

	// Initialize Telegram bot
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}

	// Initialize repositories and services
	alertsRepository := adapters.NewAlertsFirestoreRepository(firestoreClient)
	priceService := services.NewPriceService(cmcAPIKey)
	alertChecker := handlers.NewAlertChecker(alertsRepository, priceService, bot)
	telegramHandler := handlers.NewTelegramWebhookHandler(bot, alertsRepository)

	// Create HTTP server with handlers
	mux := http.NewServeMux()

	// Webhook endpoint for Telegram
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		webhookMetric.Inc()
		telegramHandler.HandleWebhook(w, r)
	})

	// Alert checker endpoint (can be triggered by Cloud Scheduler via HTTP)
	mux.HandleFunc("/check-alerts", func(w http.ResponseWriter, r *http.Request) {
		checkAlertMetric.Inc()
		log.Println("Starting price alert check...")

		if err := alertChecker.CheckAlerts(r.Context()); err != nil {
			log.Printf("Error checking alerts: %v", err)
			http.Error(w, "Error checking alerts", http.StatusInternalServerError)
			return
		}

		log.Println("Price alert check completed successfully")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Alert check completed"))
	})

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	promMux := http.NewServeMux()
	promMux.Handle("/metrics", promhttp.Handler())

	go func() {
		log.Printf("Server starting on port %s", port)
		if err := http.ListenAndServe(":"+port, mux); err != nil {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()
	log.Printf("Prometheus metrics server starting on port 8080")
	if err := http.ListenAndServe(":8080", promMux); err != nil {
		log.Fatalf("Prometheus metrics server failed to start: %v", err)
	}
}
