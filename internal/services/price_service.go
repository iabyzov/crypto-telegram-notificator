package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// PriceService handles fetching cryptocurrency prices
type PriceService struct {
	cmcAPIKey string
}

// NewPriceService creates a new PriceService
func NewPriceService(cmcAPIKey string) *PriceService {
	return &PriceService{
		cmcAPIKey: cmcAPIKey,
	}
}

const retries = 5
const retryDelay = 2 * time.Second

// GetPrice fetches the current price for a cryptocurrency symbol
func (s *PriceService) GetPrices(symbols []string) (map[string]float64, error) {
	var lastErr error
	var result *price
	for attempt := 0; attempt < retries; attempt++ {
		result, lastErr = s.fetchPrices(symbols)
		if lastErr != nil {
			time.Sleep(retryDelay)
			continue
		}
		// create a slice to hold prices
		prices := make(map[string]float64)

		for _, symbol := range symbols {
			if data, ok := result.Data[symbol]; ok {
				if quote, ok := data.Quote["USD"]; ok {
					prices[symbol] = quote.Price
				}
			}
		}
		return prices, nil
	}

	return nil, lastErr
}

type price struct {
	Data map[string]struct {
		Quote map[string]struct {
			Price float64 `json:"price"`
		} `json:"quote"`
	} `json:"data"`
}

func (s *PriceService) fetchPrices(symbols []string) (*price, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://pro-api.coinmarketcap.com/v1/cryptocurrency/quotes/latest", nil)
	if err != nil {
		return nil, err
	}

	// Join symbols into a comma-separated string
	symbolParam := ""
	for i, sym := range symbols {
		if i > 0 {
			symbolParam += ","
		}
		symbolParam += sym
	}
	q := req.URL.Query()
	q.Add("symbol", symbolParam)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("X-CMC_PRO_API_KEY", s.cmcAPIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch price: status code %d", resp.StatusCode)
	}

	var result price
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}
