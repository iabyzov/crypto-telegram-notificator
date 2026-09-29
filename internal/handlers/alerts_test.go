package handlers

// AlertChecker tests. Issue #17 made AlertChecker depend on the
// handler-local AlertsRepository interface (extended with GetAllAlerts)
// instead of the concrete Firestore repository, with no behavior change.
//
// These tests drive the full scheduled-check slice through that seam:
// alerts enter through the real webhook command path, then CheckAlerts runs
// against the real PriceService and the real BotAPI send code path. Only the
// genuine external-service boundaries are stubbed — alert storage (the
// in-memory repository the refactor unlocked), the CoinMarketCap HTTP
// endpoint (served by a local fixture through the real http client), and the
// Telegram API (the harness transport). The price cache is a redis client
// pointed at a closed local port, so every lookup misses and the real
// quotes-fetching code path executes.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
	"github.com/redis/go-redis/v9"
)

// cmcUSDQuote mirrors the CoinMarketCap quotes/latest response contract that
// services.PriceService parses.
type cmcUSDQuote struct {
	Price float64 `json:"price"`
}

type cmcSymbolQuotes struct {
	Quote map[string]cmcUSDQuote `json:"quote"`
}

type cmcQuotesResponse struct {
	Data map[string]cmcSymbolQuotes `json:"data"`
}

// cmcStub serves CoinMarketCap quotes/latest responses locally and records
// the symbol queries the real PriceService issues. It rides on a swapped
// http.DefaultTransport because PriceService builds its own http.Client
// against the hardcoded pro-api.coinmarketcap.com endpoint; the swap lets
// the real client code path run against a local fixture server.
type cmcStub struct {
	prices map[string]float64

	server *httptest.Server

	mu      sync.Mutex
	queries []string // raw symbol query parameter per request, in request order
}

func (s *cmcStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := cmcQuotesResponse{Data: map[string]cmcSymbolQuotes{}}
	for _, symbol := range strings.Split(r.URL.Query().Get("symbol"), ",") {
		if price, ok := s.prices[symbol]; ok {
			resp.Data[symbol] = cmcSymbolQuotes{Quote: map[string]cmcUSDQuote{"USD": {Price: price}}}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *cmcStub) recordRequest(rawSymbols string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, rawSymbols)
}

func (s *cmcStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

// symbolSets returns, per recorded price request, the sorted set of symbols
// the PriceService asked for.
func (s *cmcStub) symbolSets() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := make([][]string, 0, len(s.queries))
	for _, raw := range s.queries {
		symbols := strings.Split(raw, ",")
		sort.Strings(symbols)
		sets = append(sets, symbols)
	}
	return sets
}

// cmcRedirectTransport routes requests to pro-api.coinmarketcap.com to the
// stub's local server and everything else to the original transport.
type cmcRedirectTransport struct {
	stub *cmcStub
	orig http.RoundTripper
}

func (t *cmcRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "pro-api.coinmarketcap.com" {
		t.stub.recordRequest(req.URL.Query().Get("symbol"))
		redirected := req.Clone(req.Context())
		target, err := url.Parse(t.stub.server.URL + req.URL.RequestURI())
		if err != nil {
			return nil, err
		}
		redirected.URL = target
		return t.orig.RoundTrip(redirected)
	}
	return t.orig.RoundTrip(req)
}

// newCMCStub stands up the local quotes server and swaps it into
// http.DefaultTransport for the duration of the test.
func newCMCStub(t *testing.T, prices map[string]float64) *cmcStub {
	t.Helper()
	stub := &cmcStub{prices: prices}
	stub.server = httptest.NewServer(stub)
	t.Cleanup(stub.server.Close)

	orig := http.DefaultTransport
	http.DefaultTransport = &cmcRedirectTransport{stub: stub, orig: orig}
	t.Cleanup(func() { http.DefaultTransport = orig })
	return stub
}

// deadCacheClient returns a redis client pointed at a closed local port so
// every cache lookup errors; PriceService treats a cache error as a miss and
// falls through to the quotes API.
func deadCacheClient(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		MaxRetries:  -1,
		DialTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// alertCheckerHarness drives the scheduled-check slice of the product: alerts
// enter through the real webhook command path (dispatchCommand into the
// in-memory repository), then AlertChecker — constructed with that
// repository, NOT the concrete Firestore type — runs CheckAlerts against the
// real PriceService and the real BotAPI send path.
type alertCheckerHarness struct {
	*testHarness
	cmc     *cmcStub
	checker *AlertChecker
}

func newAlertCheckerHarness(t *testing.T, prices map[string]float64) *alertCheckerHarness {
	t.Helper()
	h := newTestHarness(t)
	cmc := newCMCStub(t, prices)

	// A second BotAPI on the same stub transport: GetMe is answered by the
	// stub's generic path, sendMessage requests get recorded.
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, h.transport)
	if err != nil {
		t.Fatalf("creating bot with stub transport: %v", err)
	}

	priceService := services.NewPriceService("test-cmc-key", deadCacheClient(t), time.Minute)
	checker := NewAlertChecker(h.repo, priceService, bot)
	return &alertCheckerHarness{testHarness: h, cmc: cmc, checker: checker}
}

// TestScheduledCheckNotifiesAndDeletesTriggeredAlerts drives the full cycle
// end to end: users set alerts through the bot, the scheduled check fetches
// every stored alert through the injected repository's GetAllAlerts, batches
// one price request for all symbols, notifies each triggering user, and
// deletes only the triggered alerts.
func TestScheduledCheckNotifiesAndDeletesTriggeredAlerts(t *testing.T) {
	h := newAlertCheckerHarness(t, map[string]float64{"BTC": 61234.50, "ETH": 950.25})

	// Three users set alerts through the real webhook command path.
	h.dispatchCommand(1001, "setalert", "BTC 50000 more")  // triggers at $61234.50
	h.dispatchCommand(1002, "setalert", "ETH 1000 less")   // triggers at $950.25
	h.dispatchCommand(1003, "setalert", "BTC 999999 more") // never triggers

	confirmations := h.sentMessages()
	if len(confirmations) != 3 {
		t.Fatalf("want 3 creation confirmations, got %d: %+v", len(confirmations), confirmations)
	}

	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts error: %v", err)
	}

	// All alerts, across all users, were priced with a single batched request.
	if got := h.cmc.requestCount(); got != 1 {
		t.Fatalf("want exactly 1 batched price request for all alerts, got %d", got)
	}
	sets := h.cmc.symbolSets()
	if len(sets) != 1 || !slices.Equal(sets[0], []string{"BTC", "ETH"}) {
		t.Errorf("price request must cover exactly BTC and ETH, got %v", sets)
	}

	// Exactly two notifications went out, each to the owning chat with the
	// trigger details. Notifications may arrive in any worker order.
	notifications := h.sentMessages()[len(confirmations):]
	if len(notifications) != 2 {
		t.Fatalf("want 2 notifications, got %d: %+v", len(notifications), notifications)
	}
	sort.Slice(notifications, func(i, j int) bool { return notifications[i].ChatID < notifications[j].ChatID })

	wantBTC := "🚀 Alert triggered for BTC!\nCurrent price: $61234.50\nTarget price: $50000.00 (More)\nThe price has reached or exceeded your target!"
	wantETH := "📉 Alert triggered for ETH!\nCurrent price: $950.25\nTarget price: $1000.00 (Less)\nThe price has reached or dropped below your target!"

	if notifications[0].ChatID != 1001 || notifications[0].Text != wantBTC {
		t.Errorf("BTC notification wrong:\ngot chat %d, text %q\nwant chat 1001, text %q",
			notifications[0].ChatID, notifications[0].Text, wantBTC)
	}
	if notifications[1].ChatID != 1002 || notifications[1].Text != wantETH {
		t.Errorf("ETH notification wrong:\ngot chat %d, text %q\nwant chat 1002, text %q",
			notifications[1].ChatID, notifications[1].Text, wantETH)
	}

	// Storage after the run: only the untriggered alert survives; the two
	// triggered ones were deleted.
	remaining := h.repo.added()
	if len(remaining) != 1 {
		t.Fatalf("want only the untriggered alert stored, got %d: %+v", len(remaining), remaining)
	}
	if remaining[0].UserID != 1003 || remaining[0].Symbol != "BTC" || remaining[0].TargetPrice != 999999 {
		t.Errorf("surviving alert must be user 1003's BTC at $999999, got %+v", remaining[0])
	}

	deletedByUser := map[int64]bool{}
	for _, alert := range h.repo.deleted() {
		deletedByUser[alert.UserID] = true
	}
	if len(deletedByUser) != 2 || !deletedByUser[1001] || !deletedByUser[1002] {
		t.Errorf("want exactly user 1001's and 1002's alerts deleted, got %v", deletedByUser)
	}
}

// TestScheduledCheckWithNoTriggeredAlertsKeepsAlertsAndSendsNothing pins the
// quiet path: prices are fetched, nothing fires, nothing is deleted, and the
// user hears nothing.
func TestScheduledCheckWithNoTriggeredAlertsKeepsAlertsAndSendsNothing(t *testing.T) {
	h := newAlertCheckerHarness(t, map[string]float64{"BTC": 61234.50})
	h.dispatchCommand(2001, "setalert", "BTC 999999 more")

	confirmations := len(h.sentMessages())

	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts error: %v", err)
	}

	if got := len(h.sentMessages()); got != confirmations {
		t.Errorf("no notification must be sent for an untriggered alert, got %d new messages: %+v",
			got-confirmations, h.sentMessages()[confirmations:])
	}
	if got := len(h.repo.added()); got != 1 {
		t.Errorf("the untriggered alert must stay stored, got %d alerts: %+v", got, h.repo.added())
	}
	if got := len(h.repo.deleted()); got != 0 {
		t.Errorf("nothing must be deleted, got %d deletions: %+v", got, h.repo.deleted())
	}
	if got := h.cmc.requestCount(); got != 1 {
		t.Errorf("prices are still fetched during the check, want 1 request, got %d", got)
	}
}

// TestScheduledCheckWithRepositoryFailureReturnsErrorAndTouchesNothing is
// the failure guard: when GetAllAlerts fails, the check aborts with that
// error before any price fetch, notification, or deletion happens.
func TestScheduledCheckWithRepositoryFailureReturnsErrorAndTouchesNothing(t *testing.T) {
	injected := errors.New("firestore unavailable")

	repo := &fakeAlertsRepository{fatalErr: injected}
	transport := &stubTelegramTransport{}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, transport)
	if err != nil {
		t.Fatalf("creating bot with stub transport: %v", err)
	}
	cmc := newCMCStub(t, map[string]float64{"BTC": 61234.50})
	priceService := services.NewPriceService("test-cmc-key", deadCacheClient(t), time.Minute)
	checker := NewAlertChecker(repo, priceService, bot)

	err = checker.CheckAlerts(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("CheckAlerts must fail with the repository error, got %v", err)
	}
	if !strings.Contains(err.Error(), "failed to get alerts") {
		t.Errorf("error must name the failed step, got %q", err.Error())
	}
	if got := len(transport.sentMessages()); got != 0 {
		t.Errorf("no notification must be sent, got %d: %+v", got, transport.sentMessages())
	}
	if got := len(repo.deleted()); got != 0 {
		t.Errorf("no alert must be deleted, got %d: %+v", got, repo.deleted())
	}
	if got := cmc.requestCount(); got != 0 {
		t.Errorf("the price API must not be called, got %d requests", got)
	}
}

// TestScheduledCheckWithNoStoredAlertsSucceedsWithoutPriceCalls pins the
// empty-repository boundary: the check succeeds and never spends a
// CoinMarketCap request.
func TestScheduledCheckWithNoStoredAlertsSucceedsWithoutPriceCalls(t *testing.T) {
	h := newAlertCheckerHarness(t, map[string]float64{"BTC": 61234.50})

	if err := h.checker.CheckAlerts(context.Background()); err != nil {
		t.Fatalf("CheckAlerts over an empty repository must succeed, got %v", err)
	}
	if got := h.cmc.requestCount(); got != 0 {
		t.Errorf("no price request must be made for an empty repository, got %d", got)
	}
	if got := len(h.sentMessages()); got != 0 {
		t.Errorf("no message must be sent, got %d: %+v", got, h.sentMessages())
	}
	if got := len(h.repo.deleted()); got != 0 {
		t.Errorf("nothing must be deleted, got %d: %+v", got, h.repo.deleted())
	}
}
