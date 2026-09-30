package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type PriceService struct {
	cmcAPIKey string
	// cmcEndpoint is the CoinMarketCap quotes/latest URL. Injectable so tests
	// can point the service at a fake server.
	cmcEndpoint string
	cache       *redis.Client
	cacheTTL    time.Duration
}

// NewPriceService creates a PriceService against the production CoinMarketCap
// endpoint.
func NewPriceService(cmcAPIKey string, cache *redis.Client, cacheTTL time.Duration) *PriceService {
	return NewPriceServiceWithEndpoint(cmcAPIKey, cache, cacheTTL, "https://pro-api.coinmarketcap.com/v1/cryptocurrency/quotes/latest")
}

// NewPriceServiceWithEndpoint is NewPriceService with an explicit API endpoint,
// the seam tests use to point the service at a fake CoinMarketCap server.
func NewPriceServiceWithEndpoint(cmcAPIKey string, cache *redis.Client, cacheTTL time.Duration, cmcEndpoint string) *PriceService {
	return &PriceService{
		cmcAPIKey:   cmcAPIKey,
		cmcEndpoint: cmcEndpoint,
		cache:       cache,
		cacheTTL:    cacheTTL,
	}
}

const retries = 5
const retryDelay = 2 * time.Second

type price struct {
	Data map[string]struct {
		Quote map[string]struct {
			Price float64 `json:"price"`
		} `json:"quote"`
	} `json:"data"`
}

func (s *PriceService) GetPrices(symbols []string) (map[string]float64, error) {
	ctx := context.Background()

	client := &http.Client{}
	req, err := http.NewRequest("GET", s.cmcEndpoint, nil)
	if err != nil {
		return nil, err
	}

	prices := make(map[string]float64)

	// Join symbols into a comma-separated string
	symbolParams := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		// A nil cache means caching is disabled (as in tests); every symbol
		// goes straight to the API.
		if s.cache != nil {
			val, err := s.cache.Get(ctx, sym).Result()
			if err == nil {
				intVal, _ := strconv.ParseFloat(val, 64)
				prices[sym] = intVal
				continue
			}
		}

		symbolParams = append(symbolParams, sym)
	}

	if len(symbolParams) == 0 {
		return prices, nil
	}

	q := req.URL.Query()
	q.Add("symbol", strings.Join(symbolParams, ","))
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

	for _, symbol := range symbols {
		if data, ok := result.Data[symbol]; ok {
			if quote, ok := data.Quote["USD"]; ok {
				prices[symbol] = quote.Price
				if s.cache != nil {
					s.cache.Set(ctx, symbol, strconv.FormatFloat(quote.Price, 'f', -1, 64), s.cacheTTL).Result()
				}
			}
		}
	}

	return prices, nil
}
