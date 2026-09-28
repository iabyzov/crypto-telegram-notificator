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
- `POST /webhook` — receives Telegram updates; dispatches `/setalert`, `/alert`, `/listalerts`, `/deletealert`, `/help`, `/start`
- `GET /check-alerts` — triggered by Cloud Scheduler every 5 min; checks all alerts and fires Telegram notifications for triggered ones, then deletes them
- `GET /health` — health check
- `:8080/metrics` — Prometheus metrics (separate goroutine)

**Data flow for alert checking:**
1. Cloud Scheduler → `GET /check-alerts`
2. `AlertChecker.CheckAlerts()` fetches all alerts from Firestore
3. Groups by symbol → single batch request to CoinMarketCap API
4. Compares each alert's target price against current price using the domain `PriceAlert.IsTriggeredBy` (More: at-or-above, Less: at-or-below)
5. Sends Telegram notification → deletes triggered alert from Firestore

## Environment Variables

Documented in README.md — see the "Environment Variables" section for the required and optional variables, including the optional `TELEGRAM_WEBHOOK_SECRET` for webhook secret-token verification.

## Deployment Notes

- Docker image: multi-stage build, distroless base, CGO disabled
- Service config with GMP sidecar: `run-service-with-sidecar.yaml`
- IAM policy for public access: `policy.yaml`
- `run-service-with-sidecar.yaml` contains hardcoded credentials — do not commit real values

## Agent skills

### Issue tracker

Issues live as GitHub issues in `iabyzov/crypto-telegram-notificator`, managed via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-label vocabulary; each label string equals its role name (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `CONTEXT.md` plus `docs/adr/`, created lazily by `/domain-modeling`. See `docs/agents/domain.md`.

## TDD is mandatory

Every change follows **failing test first → implement → verify**:
1. Write the test(s) that capture the desired behavior and watch them **fail** (red).
2. Implement the minimum to make them pass.
3. Run the suite + typecheck and confirm green.

Don't write implementation before a failing test exists. When fixing a bug, reproduce it with a
failing test first.

## Verify before claiming "done"

Never report something as working without running it. "Done" means: relevant tests green,
typecheck clean, and — for user-facing flows — exercised end to end (e.g. Playwright for web
flows). If tests fail or a step was skipped, say so plainly with the output.

## Validation pipeline (no-mistakes)

Ship through the gate instead of `git push origin`: `git push no-mistakes <branch>` (or the /no-mistakes skill). Then **review the PR - its evidence + risk assessment - not the raw diff**.

