package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"time"

	"cloud.google.com/go/firestore"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/adapters"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/handlers"
	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/services"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"net/http/pprof"
)

var (
	checkAlertMetric = promauto.NewCounter(prometheus.CounterOpts{
		Name: "check_alert_requests_total",
		Help: "Total number of /check-alerts endpoint invocations",
	})
	webhookMetric = promauto.NewCounter(prometheus.CounterOpts{
		Name: "webhook_requests_total",
		Help: "Total number of /webhook endpoint invocations",
	})
)

func main() {
	// Get environment variables
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is not set")
	}

	cmcAPIKey := os.Getenv("CMC_API_KEY")
	if cmcAPIKey == "" {
		log.Fatal("CMC_API_KEY environment variable is not set")
	}

	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		log.Fatal("GCP_PROJECT_ID environment variable is not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	// Initialize Firestore client
	ctx := context.Background()
	firestoreClient, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}
	defer firestoreClient.Close()

	// Initialize Telegram bot
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}

	// Initialize Redis client
	redisURL := os.Getenv("UPSTASH_REDIS_URL")
	if redisURL == "" {
		log.Fatal("UPSTASH_REDIS_URL environment variable is not set")
	}
	redisOpt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	}
	rdb := redis.NewClient(redisOpt)

	var alertLlmParser handlers.AlertIntentParser
	var llmClient *services.OpenAIClient
	llmAPIKey := os.Getenv("LLM_API_KEY")
	llmBaseUrl := os.Getenv("LLM_BASE_URL")
	llmModel := os.Getenv("LLM_MODEL")
	if llmAPIKey == "" || llmBaseUrl == "" {
		log.Printf("llm integration is disabled")
	} else {
		log.Printf("llm integration is enabled")
		llmClient = services.NewOpenAIClient(llmAPIKey, llmBaseUrl, llmModel)
		alertLlmParser = llmClient // non-nil concrete -> non-nil interface
	}

	// Initialize repositories and services
	alertsRepository := adapters.NewAlertsFirestoreRepository(firestoreClient)
	priceService := services.NewPriceService(cmcAPIKey, rdb, 60*time.Second)
	alertChecker := handlers.NewAlertChecker(alertsRepository, priceService, bot)

	// --- RAG pipeline (optional, nil-guarded) ---
	// Embeddings via Ollama /api/embed. Reuses the OpenWebUI key by default.
	var emb services.EmbeddingsClient
	embedBase := os.Getenv("EMBEDDINGS_BASE_URL")
	embedModel := os.Getenv("EMBEDDINGS_MODEL")
	embedKey := os.Getenv("EMBEDDINGS_API_KEY")
	if embedKey == "" {
		embedKey = llmAPIKey // same OpenWebUI key as chat by default
	}
	if embedBase == "" || embedModel == "" {
		log.Printf("embeddings disabled")
	} else if e, err := services.NewOllamaEmbeddings(embedBase, embedKey, embedModel); err != nil {
		log.Printf("embeddings disabled: %v", err)
	} else {
		emb = e
		log.Printf("embeddings enabled (%s)", embedModel)
	}

	// Vector store (Upstash Vector). nil when URL unset.
	var store adapters.VectorStore
	if vecURL := os.Getenv("UPSTASH_VECTOR_URL"); vecURL != "" {
		store = adapters.NewUpstashVectorStore(vecURL, os.Getenv("UPSTASH_VECTOR_TOKEN"))
		log.Printf("vector store enabled")
	} else {
		log.Printf("vector store disabled")
	}

	// Ingester: ingest asset info on alert creation. Needs embeddings + store.
	var ing *services.Ingester
	var ingester handlers.AlertIngester
	if emb != nil && store != nil {
		infoSvc := services.NewAssetInfoService(cmcAPIKey, rdb, 24*time.Hour)
		ing = services.NewIngester(infoSvc, emb, store)
		ingester = ing // non-nil concrete -> non-nil interface
		log.Printf("ingester enabled")
	} else {
		log.Printf("ingester disabled")
	}

	// RAG service: grounded Q&A. Needs llm + embeddings + store; ingester optional.
	var research services.ResearchService
	if llmClient != nil && emb != nil && store != nil {
		research = services.NewRAGService(emb, store, llmClient, ing)
		log.Printf("rag enabled")
	} else {
		log.Printf("rag disabled")
	}

	telegramHandler := handlers.NewTelegramWebhookHandler(bot, alertsRepository, alertLlmParser, research, ingester)

	// Create HTTP server with handlers
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// Webhook endpoint for Telegram
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		webhookMetric.Inc()
		telegramHandler.HandleWebhook(w, r)
	})

	// Alert checker endpoint (can be triggered by Cloud Scheduler via HTTP)
	mux.HandleFunc("/check-alerts", func(w http.ResponseWriter, r *http.Request) {
		checkAlertMetric.Inc()
		log.Println("Starting price alert check...")

		if err := alertChecker.CheckAlerts(r.Context()); err != nil {
			log.Printf("Error checking alerts: %v", err)
			http.Error(w, "Error checking alerts", http.StatusInternalServerError)
			return
		}

		log.Println("Price alert check completed successfully")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Alert check completed"))
	})

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	promMux := http.NewServeMux()
	promMux.Handle("/metrics", promhttp.Handler())

	go func() {
		log.Printf("Server starting on port %s", port)
		if err := http.ListenAndServe(":"+port, mux); err != nil {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()
	log.Printf("Prometheus metrics server starting on port 8080")
	if err := http.ListenAndServe(":8080", promMux); err != nil {
		log.Fatalf("Prometheus metrics server failed to start: %v", err)
	}
}
