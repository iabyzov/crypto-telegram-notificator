# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Philosophy

This project is used as a learning playground. It's fine — and encouraged — to introduce new libraries or frameworks when they add value or serve as an opportunity to explore them.

## What This Is

A Telegram bot that monitors cryptocurrency prices (via CoinMarketCap API) and sends alerts when user-defined target prices are met. Deployed as a Google Cloud Run service with Firestore persistence and Cloud Scheduler for periodic price checks.

## Commands

```bash
# Build
go build -v ./...

# Test
go test ./...

# Vet
go vet ./...

# Docker build & push
./build-deploy-docker.sh

# Deploy to Cloud Run (source-based)
gcloud run deploy crypto-telegram-notificator \
  --source=. --region=us-central1 --allow-unauthenticated \
  --set-env-vars TELEGRAM_BOT_TOKEN=...,CMC_API_KEY=...,GCP_PROJECT_ID=...
```

## Architecture

**Clean architecture with four layers:**

- `internal/domain/alerts/` — domain models (`PriceAlert`, `AlertType` enum: `More`/`Less`)
- `internal/adapters/` — Firestore repository implementing `AlertsRepository` interface
- `internal/services/` — `PriceService` batches CoinMarketCap API calls by grouping alerts by symbol
- `internal/handlers/` — two handlers: `TelegramWebhookHandler` (incoming bot commands) and `AlertChecker` (scheduled price evaluation)

**HTTP endpoints registered in `main.go`:**
- `POST /webhook` — receives Telegram updates; dispatches `/setalert`, `/listalerts`, `/deletealert`, `/help`, `/start`
- `GET /check-alerts` — triggered by Cloud Scheduler every 5 min; checks all alerts and fires Telegram notifications for triggered ones, then deletes them
- `GET /health` — health check
- `:8080/metrics` — Prometheus metrics (separate goroutine)

**Data flow for alert checking:**
1. Cloud Scheduler → `GET /check-alerts`
2. `AlertChecker.CheckAlerts()` fetches all alerts from Firestore
3. Groups by symbol → single batch request to CoinMarketCap API
4. Compares each alert's target price against current price using `AlertType` (More=above, Less=below)
5. Sends Telegram notification → deletes triggered alert from Firestore

## Required Environment Variables

| Variable | Purpose |
|---|---|
| `TELEGRAM_BOT_TOKEN` | Telegram bot auth token |
| `CMC_API_KEY` | CoinMarketCap API key |
| `GCP_PROJECT_ID` | Google Cloud project ID |
| `PORT` | HTTP server port (defaults to 8080) |

## Deployment Notes

- Docker image: multi-stage build, distroless base, CGO disabled
- Service config with GMP sidecar: `run-service-with-sidecar.yaml`
- IAM policy for public access: `policy.yaml`
- `run-service-with-sidecar.yaml` contains hardcoded credentials — do not commit real values
