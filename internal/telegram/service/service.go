package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// Server handles bot logic and price monitoring
type Server struct {
	Bot              *tgbotapi.BotAPI
	alerts           map[string][]alerts.PriceAlert
	alertsMutex      sync.RWMutex
	updateChan       tgbotapi.UpdatesChannel
	alertsRepository adapters.AlertsFirestoreRepository
}

func NewServer(botToken string, alertsRepository adapters.AlertsFirestoreRepository) (*Server, error) {
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, err
	}
	return &Server{
		Bot:              bot,
		alerts:           make(map[string][]alerts.PriceAlert),
		alertsRepository: alertsRepository,
	}, nil
}

// Start polling mode
func (s *Server) Start() {
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60
	updates := s.Bot.GetUpdatesChan(updateConfig)
	for update := range updates {
		if update.Message == nil {
			continue
		}
		go s.handleMessage(update.Message)
	}
}

// SetupWebhook registers the webhook URL with Telegram
func (s *Server) SetupWebhook(webhookURL string) error {
	wh, _ := tgbotapi.NewWebhook(webhookURL + "/webhook")
	_, err := s.Bot.Request(wh)
	return err
}

// HandleWebhook processes incoming webhook HTTP requests from Telegram
func (s *Server) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "invalid method", http.StatusMethodNotAllowed)
		return
	}
	var update tgbotapi.Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if update.Message != nil {
		go s.handleMessage(update.Message)
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleMessage(message *tgbotapi.Message) {
	if !message.IsCommand() {
		s.sendMessage(message.Chat.ID, "Please use commands to interact with the bot. Type /help for available commands.")
		return
	}

	switch message.Command() {
	case "start", "help":
		s.handleHelp(message)
	case "setalert":
		s.handleSetAlert(message)

	default:
		s.sendMessage(message.Chat.ID, "Unknown command. Type /help for available commands.")
	}
}

func (s *Server) handleHelp(message *tgbotapi.Message) {
	helpText := `Available commands:
/setalert <symbol> <price> - Set price alert for cryptocurrency
/deletealert <symbol> - Delete price alert for cryptocurrency
/listalerts - List all your active alerts
/price <symbol> - Get current price for cryptocurrency
/help - Show this help message

Example:
/setalert BTC 50000 - Alert when Bitcoin price exceeds $50,000`

	s.sendMessage(message.Chat.ID, helpText)
}

func (s *Server) handleSetAlert(message *tgbotapi.Message) {

	args := strings.Fields(message.CommandArguments())
	if len(args) != 3 {
		s.sendMessage(message.Chat.ID, "Invalid format. Use: /setalert <symbol> <price> <above/below>")
		return
	}
	symbol := strings.ToUpper(args[0])
	price, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		s.sendMessage(message.Chat.ID, "Invalid price value. Please enter a valid number.")
		return
	}
	alertType, _ := ParseAlertType(args[2])

	alert := alerts.PriceAlert{
		Symbol:      symbol,
		TargetPrice: price, // parsed threshold
		UserID:      message.Chat.ID,
		Type:        alertType,
	}

	// Save alert to Firestore

	ctx := context.Background()
	s.alertsRepository.AddAlert(ctx, alert)

	s.alertsMutex.Lock()
	s.alerts[alert.Symbol] = append(s.alerts[alert.Symbol], alert)
	s.alertsMutex.Unlock()

	s.sendMessage(message.Chat.ID, fmt.Sprintf("Alert set for %s at $%.2f", alert.Symbol, alert.TargetPrice))
}

func ParseAlertType(s string) (alerts.AlertType, error) {
	for i := alerts.More; i <= alerts.Less; i++ {
		if strings.EqualFold(s, i.String()) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("invalid number: %s", s)
}

func (s *Server) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := s.Bot.Send(msg); err != nil {
		log.Printf("Error sending message: %v", err)
	}
}
