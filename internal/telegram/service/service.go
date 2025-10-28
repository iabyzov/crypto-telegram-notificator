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
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

// AlertsRepository defines the interface for alert storage operations
type AlertsRepository interface {
	AddAlert(ctx context.Context, alert alerts.PriceAlert)
	GetAlertsByUserID(ctx context.Context, userID int64) ([]alerts.PriceAlert, error)
	DeleteAlert(ctx context.Context, alert alerts.PriceAlert) error
}

// Server handles bot logic and price monitoring
type Server struct {
	Bot              *tgbotapi.BotAPI
	alerts           map[string][]alerts.PriceAlert
	alertsMutex      sync.RWMutex
	updateChan       tgbotapi.UpdatesChannel
	alertsRepository AlertsRepository
}

func NewServer(botToken string, alertsRepository AlertsRepository) (*Server, error) {
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
	case "listalerts":
		s.handleListAlerts(message)
	case "deletealert":
		s.handleDeleteAlert(message)
	default:
		s.sendMessage(message.Chat.ID, "Unknown command. Type /help for available commands.")
	}
}

func (s *Server) handleHelp(message *tgbotapi.Message) {
	helpText := `Available commands:
/setalert <symbol> <price> <above|below> - Set price alert for cryptocurrency
/listalerts - List all your active alerts
/deletealert <alert_id> - Delete a specific alert by ID
/help - Show this help message

Example:
/setalert BTC 50000 above - Alert when Bitcoin price exceeds $50,000`

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

func (s *Server) handleListAlerts(message *tgbotapi.Message) {
	ctx := context.Background()
	userAlerts, err := s.alertsRepository.GetAlertsByUserID(ctx, message.Chat.ID)
	if err != nil {
		s.sendMessage(message.Chat.ID, "Failed to retrieve alerts. Please try again later.")
		log.Printf("Error retrieving alerts for user %d: %v", message.Chat.ID, err)
		return
	}

	if len(userAlerts) == 0 {
		s.sendMessage(message.Chat.ID, "You have no active alerts.")
		return
	}

	var response strings.Builder
	response.WriteString("Your active alerts:\n\n")
	for i, alert := range userAlerts {
		typeStr := "above"
		emoji := "🚀"
		if alert.Type == alerts.Less {
			typeStr = "below"
			emoji = "📉"
		}
		response.WriteString(fmt.Sprintf("%d. %s %s %s at $%.2f\n   ID: %s\n\n",
			i+1, emoji, alert.Symbol, typeStr, alert.TargetPrice, alert.Id))
	}
	response.WriteString("Use /deletealert <ID> to remove an alert.")

	s.sendMessage(message.Chat.ID, response.String())
}

func (s *Server) handleDeleteAlert(message *tgbotapi.Message) {
	args := strings.Fields(message.CommandArguments())
	if len(args) != 1 {
		s.sendMessage(message.Chat.ID, "Invalid format. Use: /deletealert <alert_id>")
		return
	}
	alertID := args[0]

	ctx := context.Background()
	
	// First, verify the alert exists and belongs to the user
	userAlerts, err := s.alertsRepository.GetAlertsByUserID(ctx, message.Chat.ID)
	if err != nil {
		s.sendMessage(message.Chat.ID, "Failed to retrieve alerts. Please try again later.")
		log.Printf("Error retrieving alerts for user %d: %v", message.Chat.ID, err)
		return
	}

	var alertToDelete *alerts.PriceAlert
	for _, alert := range userAlerts {
		if alert.Id == alertID {
			alertToDelete = &alert
			break
		}
	}

	if alertToDelete == nil {
		s.sendMessage(message.Chat.ID, "Alert not found. Use /listalerts to see your alerts.")
		return
	}

	// Delete the alert
	err = s.alertsRepository.DeleteAlert(ctx, *alertToDelete)
	if err != nil {
		s.sendMessage(message.Chat.ID, "Failed to delete alert. Please try again later.")
		log.Printf("Error deleting alert %s for user %d: %v", alertID, message.Chat.ID, err)
		return
	}

	s.sendMessage(message.Chat.ID, fmt.Sprintf("Alert deleted: %s at $%.2f", alertToDelete.Symbol, alertToDelete.TargetPrice))
}

func (s *Server) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := s.Bot.Send(msg); err != nil {
		log.Printf("Error sending message: %v", err)
	}
}
