package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const testCMCAPIKey = "test-cmc-key"

// cmcRecorder records every quotes/latest request a fake CoinMarketCap
// server receives: the symbol batch (the API's query param is one
// comma-joined string) and the request headers.
type cmcRecorder struct {
	mu      sync.Mutex
	batches [][]string
	apiKeys []string
	accepts []string
}

func (r *cmcRecorder) record(req *http.Request) {
	var symbols []string
	for _, param := range req.URL.Query()["symbol"] {
		symbols = append(symbols, strings.Split(param, ",")...)
	}
	r.mu.Lock()
	r.batches = append(r.batches, symbols)
	r.apiKeys = append(r.apiKeys, req.Header.Get("X-CMC_PRO_API_KEY"))
	r.accepts = append(r.accepts, req.Header.Get("Accept"))
	r.mu.Unlock()
}

func (r *cmcRecorder) snapshot() (batches [][]string, apiKeys []string, accepts []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.batches...),
		append([]string(nil), r.apiKeys...),
		append([]string(nil), r.accepts...)
}

// newQuoteCMC starts a fake CoinMarketCap quotes/latest server that quotes
// every requested symbol at price, recording each request.
func newQuoteCMC(t *testing.T, price float64) (*httptest.Server, *cmcRecorder) {
	t.Helper()

	rec := &cmcRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		// Mirrors the quotes/latest response shape:
		// {"data":{"BTC":{"quote":{"USD":{"price":...}}}}}
		data := make(map[string]map[string]map[string]map[string]float64, 4)
		for _, param := range r.URL.Query()["symbol"] {
			for _, symbol := range strings.Split(param, ",") {
				data[symbol] = map[string]map[string]map[string]float64{
					"quote": {"USD": {"price": price}},
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)
	return server, rec
}

// newTestCache starts an in-memory Redis (miniredis) and returns a go-redis
// client against it, plus the server handle tests use to advance time.
func newTestCache(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, mr
}

// newPriceService builds a PriceService against the fake endpoint, with the
// given cache (nil disables caching, as the checker tests do).
func newPriceService(t *testing.T, endpoint string, cache *redis.Client) *PriceService {
	t.Helper()

	return NewPriceServiceWithEndpoint(testCMCAPIKey, cache, time.Minute, endpoint)
}

// formatPrice renders a price the way the cache stores it, so tests can
// compare against (or seed) cache values.
func formatPrice(price float64) string {
	return strconv.FormatFloat(price, 'f', -1, 64)
}

// TestGetPricesBatchesSymbolsInOneRequest pins the pricing quota contract:
// every requested symbol rides one batched request, sent as a single
// comma-joined query param, authenticated with the configured API key.
func TestGetPricesBatchesSymbolsInOneRequest(t *testing.T) {
	server, rec := newQuoteCMC(t, 51000.5)
	svc := newPriceService(t, server.URL, nil)

	prices, err := svc.GetPrices([]string{"BTC", "ETH"})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}

	if got, want := len(prices), 2; got != want {
		t.Fatalf("expected %d prices, got %d: %v", want, got, prices)
	}
	if got, want := prices["BTC"], 51000.5; got != want {
		t.Errorf("BTC price = %v, want %v", got, want)
	}
	if got, want := prices["ETH"], 51000.5; got != want {
		t.Errorf("ETH price = %v, want %v", got, want)
	}

	batches, apiKeys, accepts := rec.snapshot()
	if got, want := len(batches), 1; got != want {
		t.Fatalf("expected %d API request, got %d", want, got)
	}
	if got, want := batches[0], []string{"BTC", "ETH"}; !equalStrings(got, want) {
		t.Errorf("request batch = %v, want %v", got, want)
	}
	if got, want := apiKeys[0], testCMCAPIKey; got != want {
		t.Errorf("X-CMC_PRO_API_KEY = %q, want %q", got, want)
	}
	if got, want := accepts[0], "application/json"; got != want {
		t.Errorf("Accept = %q, want %q", got, want)
	}
}

// TestGetPricesOmitsUnquotedSymbols pins that a symbol the API does not quote
// is simply absent from the result, not zero-priced.
func TestGetPricesOmitsUnquotedSymbols(t *testing.T) {
	server, _ := newQuoteCMC(t, 51000)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			// Only BTC is quoted, whatever was asked.
			"data": map[string]any{
				"BTC": map[string]any{"quote": map[string]any{"USD": map[string]any{"price": 51000}}},
			},
		})
	})
	svc := newPriceService(t, server.URL, nil)

	prices, err := svc.GetPrices([]string{"BTC", "DOGE"})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}

	if _, ok := prices["DOGE"]; ok {
		t.Errorf("DOGE should be absent when the API does not quote it, got %v", prices["DOGE"])
	}
	if got, ok := prices["BTC"]; !ok || got != 51000 {
		t.Errorf("BTC price = %v (present: %v), want 51000", got, ok)
	}
}

// TestGetPricesReturnsErrorOnBadStatus pins that a non-200 response fails
// the whole call with the status code in the error.
func TestGetPricesReturnsErrorOnBadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota exceeded", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	svc := newPriceService(t, server.URL, nil)

	_, err := svc.GetPrices([]string{"BTC"})
	if err == nil {
		t.Fatal("expected an error for a non-200 response, got nil")
	}
	if want := "status code 429"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should mention %q", err.Error(), want)
	}
}

// TestGetPricesReturnsErrorOnInvalidJSON pins that a non-JSON body fails the
// call instead of yielding empty prices.
func TestGetPricesReturnsErrorOnInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	t.Cleanup(server.Close)
	svc := newPriceService(t, server.URL, nil)

	if _, err := svc.GetPrices([]string{"BTC"}); err == nil {
		t.Fatal("expected an error for a non-JSON body, got nil")
	}
}

// TestGetPricesReturnsErrorOnInvalidEndpoint pins that a malformed endpoint
// URL fails the call at request construction time.
func TestGetPricesReturnsErrorOnInvalidEndpoint(t *testing.T) {
	svc := NewPriceServiceWithEndpoint(testCMCAPIKey, nil, time.Minute, "://missing-scheme")

	if _, err := svc.GetPrices([]string{"BTC"}); err == nil {
		t.Fatal("expected an error for a malformed endpoint URL, got nil")
	}
}

// TestGetPricesServesCacheHitsWithoutAPIRequest pins the quota saving of the
// cache: a symbol the cache holds is priced without any API traffic.
func TestGetPricesServesCacheHitsWithoutAPIRequest(t *testing.T) {
	server, rec := newQuoteCMC(t, 51000)
	cache, _ := newTestCache(t)
	svc := newPriceService(t, server.URL, cache)

	if err := cache.Set(t.Context(), "BTC", formatPrice(12345.6), time.Minute).Err(); err != nil {
		t.Fatalf("seeding cache: %v", err)
	}

	prices, err := svc.GetPrices([]string{"BTC"})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}

	if got, want := prices["BTC"], 12345.6; got != want {
		t.Errorf("BTC price = %v, want cached %v", got, want)
	}
	if batches, _, _ := rec.snapshot(); len(batches) != 0 {
		t.Errorf("cached symbol should not hit the API, got batches %v", batches)
	}
}

// TestGetPricesWritesFetchedPricesToCache pins the write-back: a fetched
// price lands in the cache formatted so a later call reads it back, and the
// second call is served entirely from the cache.
func TestGetPricesWritesFetchedPricesToCache(t *testing.T) {
	server, rec := newQuoteCMC(t, 51000)
	cache, _ := newTestCache(t)
	svc := newPriceService(t, server.URL, cache)

	first, err := svc.GetPrices([]string{"BTC"})
	if err != nil {
		t.Fatalf("first GetPrices: %v", err)
	}

	val, err := cache.Get(t.Context(), "BTC").Result()
	if err != nil {
		t.Fatalf("BTC should be cached after a fetch: %v", err)
	}
	if got, want := val, formatPrice(first["BTC"]); got != want {
		t.Errorf("cached value = %q, want %q", got, want)
	}

	second, err := svc.GetPrices([]string{"BTC"})
	if err != nil {
		t.Fatalf("second GetPrices: %v", err)
	}
	if got, want := second["BTC"], first["BTC"]; got != want {
		t.Errorf("second BTC price = %v, want %v", got, want)
	}
	if batches, _, _ := rec.snapshot(); len(batches) != 1 {
		t.Errorf("second call should be served from the cache, got %d API requests", len(batches))
	}
}

// TestGetPricesFetchesOnlyCacheMisses pins the batching interaction with the
// cache: cached symbols ride out of the request, so only misses spend quota.
func TestGetPricesFetchesOnlyCacheMisses(t *testing.T) {
	server, rec := newQuoteCMC(t, 51000)
	cache, _ := newTestCache(t)
	svc := newPriceService(t, server.URL, cache)

	if err := cache.Set(t.Context(), "BTC", formatPrice(42000), time.Minute).Err(); err != nil {
		t.Fatalf("seeding cache: %v", err)
	}

	prices, err := svc.GetPrices([]string{"BTC", "ETH"})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}

	if got, want := prices["BTC"], 42000.0; got != want {
		t.Errorf("BTC price = %v, want cached %v", got, want)
	}
	if got, want := prices["ETH"], 51000.0; got != want {
		t.Errorf("ETH price = %v, want fetched %v", got, want)
	}

	batches, _, _ := rec.snapshot()
	if got, want := len(batches), 1; got != want {
		t.Fatalf("expected %d API request, got %d", want, got)
	}
	if got, want := batches[0], []string{"ETH"}; !equalStrings(got, want) {
		t.Errorf("request batch = %v, want only the misses %v", got, want)
	}
}

// TestGetPricesRefetchesAfterCacheTTLExpiry pins that cached prices honor the
// configured TTL: once the entry expires, the symbol is priced from the API
// again instead of serving a stale quote forever.
func TestGetPricesRefetchesAfterCacheTTLExpiry(t *testing.T) {
	server, rec := newQuoteCMC(t, 51000)
	cache, mr := newTestCache(t)
	svc := NewPriceServiceWithEndpoint(testCMCAPIKey, cache, time.Second, server.URL)

	if _, err := svc.GetPrices([]string{"BTC"}); err != nil {
		t.Fatalf("first GetPrices: %v", err)
	}
	if batches, _, _ := rec.snapshot(); len(batches) != 1 {
		t.Fatalf("expected the first call to fetch, got %d requests", len(batches))
	}

	// Fresh quote: the price has changed since the cached one.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"BTC": map[string]any{"quote": map[string]any{"USD": map[string]any{"price": 99999}}},
			},
		})
	})

	mr.FastForward(2 * time.Second)

	prices, err := svc.GetPrices([]string{"BTC"})
	if err != nil {
		t.Fatalf("GetPrices after TTL: %v", err)
	}
	if got, want := prices["BTC"], 99999.0; got != want {
		t.Errorf("BTC price after TTL = %v, want the fresh quote %v", got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
