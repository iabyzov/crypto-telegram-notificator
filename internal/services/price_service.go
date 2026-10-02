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

// cmcQuoteResponse is the quotes/latest wire shape:
// {"data":{"BTC":{"quote":{"USD":{"price":...}}}}}.
type cmcQuoteResponse struct {
	Data map[string]cmcSymbolQuotes `json:"data"`
}

type cmcSymbolQuotes struct {
	Quote map[string]cmcFiatQuote `json:"quote"`
}

type cmcFiatQuote struct {
	Price float64 `json:"price"`
}

// GetPrices returns the current USD price of every symbol it can price.
// Symbols the cache already holds are served without any API traffic; the
// rest are fetched together in one batched CoinMarketCap request and written
// back to the cache. A symbol the API does not quote is absent from the
// result.
func (s *PriceService) GetPrices(symbols []string) (map[string]float64, error) {
	prices := make(map[string]float64)

	// Serve what the cache knows and collect the misses, so the whole
	// remainder is priced by one batched request.
	misses := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if price, ok := s.cachedPrice(symbol); ok {
			prices[symbol] = price
			continue
		}
		misses = append(misses, symbol)
	}
	if len(misses) == 0 {
		return prices, nil
	}

	fetched, err := s.fetchPrices(misses)
	if err != nil {
		return nil, err
	}
	for _, symbol := range misses {
		if price, ok := fetched[symbol]; ok {
			prices[symbol] = price
			s.cachePrice(symbol, price)
		}
	}
	return prices, nil
}

// cachedPrice returns the price stored under symbol, reporting false when
// there is none to serve. A nil cache means caching is disabled, so every
// symbol is a miss. A cached value that fails to parse reads as zero: the
// cache only ever holds values this service formatted itself.
func (s *PriceService) cachedPrice(symbol string) (float64, bool) {
	if s.cache == nil {
		return 0, false
	}
	val, err := s.cache.Get(context.Background(), symbol).Result()
	if err != nil {
		return 0, false
	}
	price, _ := strconv.ParseFloat(val, 64)
	return price, true
}

// cachePrice stores a freshly fetched price under symbol for the service's
// TTL. A nil cache means caching is disabled: nothing to store.
func (s *PriceService) cachePrice(symbol string, price float64) {
	if s.cache == nil {
		return
	}
	s.cache.Set(context.Background(), symbol, strconv.FormatFloat(price, 'f', -1, 64), s.cacheTTL)
}

// fetchPrices asks the CoinMarketCap quotes endpoint for all symbols in one
// batched request — symbols travel as a single comma-joined query param —
// and returns the USD price of every symbol the API quotes, keyed by symbol.
func (s *PriceService) fetchPrices(symbols []string) (map[string]float64, error) {
	req, err := http.NewRequest("GET", s.cmcEndpoint, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("symbol", strings.Join(symbols, ","))
	req.URL.RawQuery = q.Encode()

	req.Header.Set("X-CMC_PRO_API_KEY", s.cmcAPIKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch price: status code %d", resp.StatusCode)
	}

	var result cmcQuoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	prices := make(map[string]float64, len(symbols))
	for _, symbol := range symbols {
		if quotes, ok := result.Data[symbol]; ok {
			if usd, ok := quotes.Quote["USD"]; ok {
				prices[symbol] = usd.Price
			}
		}
	}
	return prices, nil
}
