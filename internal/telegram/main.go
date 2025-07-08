package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"cloud.google.com/go/firestore"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/telegram/service"
)

func main() {
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

	server, err := service.NewServer(botToken, alertsRepository)
	if err != nil {
		log.Fatal(err)
	}

	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL != "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		if err := server.SetupWebhook(webhookURL); err != nil {
			log.Fatalf("Failed to set webhook: %v", err)
		}
		http.HandleFunc("/webhook", server.HandleWebhook)
		log.Printf("Bot started in webhook mode: @%s, listening on :%s", server.Bot.Self.UserName, port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	} else {
		log.Printf("Bot started in polling mode: @%s", server.Bot.Self.UserName)
		server.Start()
	}
}
