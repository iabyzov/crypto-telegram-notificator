package handlers

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/events"
)

type pubSubEnvelope struct {
	Message struct {
		Data      []byte `json:"data"`
		MessageID string `json:"messageId"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

type NotificationHandler struct{}

func NewNotificationHandler() *NotificationHandler {
	return &NotificationHandler{}
}

func (h *NotificationHandler) HandleNotification(w http.ResponseWriter, r *http.Request) {
	var envelope pubSubEnvelope
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var event events.AlertTriggeredEvent
	if err := json.Unmarshal(envelope.Message.Data, &event); err != nil {
		log.Printf("Failed to unmarshal alert event: %v", err)
		// Return 200 so Pub/Sub does not retry a malformed message indefinitely
		w.WriteHeader(http.StatusOK)
		return
	}

	log.Printf("Received alert event via Pub/Sub: symbol=%s userId=%d currentPrice=%.2f targetPrice=%.2f",
		event.Symbol, event.UserId, event.CurrentPrice, event.TargetPrice)

	w.WriteHeader(http.StatusOK)
}
