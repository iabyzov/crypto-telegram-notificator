package function

import (
	"context"
	"log"
	"net/http"
	"os"

	"cloud.google.com/go/firestore"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/handlers"
)

func Handler(w http.ResponseWriter, r *http.Request) {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is not set")
	}

	ctx := context.Background()
	firestoreClient, err := firestore.NewClient(ctx, os.Getenv("GCP_PROJECT_ID"))
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	alertsRepository := adapters.NewAlertsFirestoreRepository(firestoreClient)

	tgBotApi, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot client: %v", err)
	}

	telegramHandler := handlers.NewTelegramWebhookHandler(tgBotApi, alertsRepository)

	telegramHandler.HandleWebhook(w, r)
}
