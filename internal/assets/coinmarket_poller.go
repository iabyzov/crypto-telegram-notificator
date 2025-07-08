package assets

import (
	"encoding/json"
	"net/http"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

type Server struct {
	alerts    map[string][]PriceAlert
	cmcAPIKey string
}

type PriceAlert struct {
	Symbol    string
	Threshold float64
	ChatID    int64
	Type      alerts.AlertType
}

func NewServer(cmcAPIKey string) (*Server, error) {
	return &Server{
		alerts:    make(map[string][]PriceAlert),
		cmcAPIKey: cmcAPIKey,
	}, nil
}

func (s *Server) getPrice(symbol string) (float64, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://pro-api.coinmarketcap.com/v1/cryptocurrency/quotes/latest", nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Add("symbol", symbol)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("X-CMC_PRO_API_KEY", s.cmcAPIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result struct {
		Data map[string]struct {
			Quote map[string]struct {
				Price float64 `json:"price"`
			} `json:"quote"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	return result.Data[symbol].Quote["USD"].Price, nil
}
