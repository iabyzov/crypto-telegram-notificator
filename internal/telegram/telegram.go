package telegram

import (
	"context"
	"log"
	"net/http"
	"os"

	"cloud.google.com/go/firestore"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/telegram/service"
)

var server *service.Server

func init() {
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

	server, err = service.NewServer(botToken, alertsRepository)
	if err != nil {
		log.Fatal(err)
	}
}

// HandleWebhook is the entry point for Google Cloud Functions
func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	server.HandleWebhook(w, r)
}
