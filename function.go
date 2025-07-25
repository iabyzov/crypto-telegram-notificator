package function

import (
	"net/http"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/telegram"
)

func Handler(w http.ResponseWriter, r *http.Request) {
	telegram.HandleWebhook(w, r)
}
