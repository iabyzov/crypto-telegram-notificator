package alertjob

import (
	"context"
	"log"
	"os"

	"cloud.google.com/go/firestore"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
)

var (
	alertChecker *services.AlertChecker
)

func init() {
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

	ctx := context.Background()

	// Initialize Firestore client
	firestoreClient, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}

	// Initialize Telegram bot
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}

	// Initialize repositories and services
	alertsRepository := adapters.NewAlertsFirestoreRepository(firestoreClient)
	priceService := services.NewPriceService(cmcAPIKey)
	alertChecker = services.NewAlertChecker(alertsRepository, priceService, bot)

	log.Println("Alert job initialized successfully")
}

// CheckPriceAlerts is the Cloud Function entry point for checking price alerts
func CheckPriceAlerts(ctx context.Context, m interface{}) error {
	log.Println("Starting price alert check...")
	
	if err := alertChecker.CheckAlerts(ctx); err != nil {
		log.Printf("Error checking alerts: %v", err)
		return err
	}
	
	log.Println("Price alert check completed successfully")
	return nil
}
