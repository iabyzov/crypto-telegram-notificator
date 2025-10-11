# Implementation Summary: Price Alert Tracking

## Overview
Implemented a scheduled Google Cloud Function that checks cryptocurrency price alerts every 5 minutes and notifies users via Telegram when their target prices are reached.

## Changes Made

### 1. Repository Layer Enhancement
**File:** `internal/adapters/alerts_firestore_repository.go`
- Added `GetAllAlerts()` method to retrieve all alerts from Firestore
- Added `mapToDomainModel()` helper to convert Firestore models to domain models
- Both methods handle error cases gracefully

### 2. Price Service
**File:** `internal/services/price_service.go` (NEW)
- `PriceService` struct for fetching cryptocurrency prices
- `GetPrice(symbol)` method that calls CoinMarketCap API
- Returns current USD price for any cryptocurrency symbol
- Includes error handling for API failures

### 3. Alert Checker Service
**File:** `internal/services/alert_checker.go` (NEW)
- `AlertChecker` struct that orchestrates the alert checking process
- `CheckAlerts(ctx)` main method that:
  - Fetches all alerts from Firestore
  - Groups alerts by symbol to minimize API calls
  - Fetches current price for each symbol
  - Checks if alert conditions are met
  - Sends notifications for triggered alerts
- `isAlertTriggered()` checks if price meets alert condition (above/below)
- `sendNotification()` sends formatted Telegram messages with emojis

### 4. Alert Job Package
**File:** `internal/alertjob/alertjob.go` (NEW)
- Initializes all required services (Firestore, Telegram bot, price service)
- `CheckPriceAlerts()` function serves as the Cloud Function handler
- Loads configuration from environment variables

### 5. Cloud Function Entry Point
**File:** `alertjob_function.go` (NEW)
- Top-level Cloud Function entry point for the scheduled job
- Delegates to `alertjob.CheckPriceAlerts()`
- Follows Google Cloud Functions naming conventions

### 6. Documentation
**File:** `README.md` (NEW)
- Complete deployment guide for both Cloud Functions
- Instructions for setting up Cloud Scheduler
- Usage examples for end users
- Troubleshooting section
- Architecture overview

## Architecture

```
Cloud Scheduler (every 5 min)
    ↓
Pub/Sub Topic (price-alerts-trigger)
    ↓
Cloud Function (CheckPriceAlerts)
    ↓
AlertChecker Service
    ├── Firestore (fetch alerts)
    ├── CoinMarketCap API (fetch prices)
    └── Telegram API (send notifications)
```

## Deployment Steps

1. Deploy Telegram webhook handler (existing function)
2. Deploy alert checker function with Pub/Sub trigger
3. Create Pub/Sub topic: `price-alerts-trigger`
4. Create Cloud Scheduler job with cron: `*/5 * * * *`

## Key Features

- ✅ Fetches all alerts from Firestore
- ✅ Groups by symbol to minimize API calls
- ✅ Checks both "above" and "below" alert types
- ✅ Sends formatted notifications with emojis
- ✅ Comprehensive error handling and logging
- ✅ Runs every 5 minutes via Cloud Scheduler
- ✅ Minimal changes to existing codebase

## Testing

Build verification:
```bash
go build -v ./...
go vet ./...
```

Both commands complete successfully with no errors.

## Notes

- Alert checking is idempotent (safe to run multiple times)
- API rate limits: CoinMarketCap free tier allows 333 requests/day
- Alerts are NOT deleted after triggering (future enhancement needed)
- No tests added as per minimal change requirement
