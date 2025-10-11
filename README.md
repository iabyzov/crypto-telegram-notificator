# Crypto Telegram Notificator

A Telegram bot that monitors cryptocurrency prices and sends alerts when price targets are met.

## Features

- Set price alerts for cryptocurrencies (above/below target prices)
- Automated price monitoring via scheduled Cloud Function
- Real-time notifications via Telegram
- Data persistence using Google Cloud Firestore

## Architecture

The application consists of two Google Cloud Functions:

1. **Telegram Webhook Handler** (`function.go`): Handles incoming messages from users via Telegram webhook
2. **Alert Checker Job** (`alertjob_function.go`): Scheduled job that checks price alerts every 5 minutes

## Prerequisites

- Google Cloud Platform account
- Telegram Bot Token (get from [@BotFather](https://t.me/botfather))
- CoinMarketCap API Key (get from [CoinMarketCap API](https://coinmarketcap.com/api/))
- Go 1.22.3 or later

## Environment Variables

Both functions require the following environment variables:

- `TELEGRAM_BOT_TOKEN`: Your Telegram bot token
- `CMC_API_KEY`: Your CoinMarketCap API key
- `GCP_PROJECT_ID`: Your Google Cloud Project ID

## Deployment

### 1. Deploy the Telegram Webhook Handler

```bash
gcloud functions deploy telegram-webhook \
  --gen2 \
  --runtime=go122 \
  --region=us-central1 \
  --source=. \
  --entry-point=Handler \
  --trigger-http \
  --allow-unauthenticated \
  --set-env-vars TELEGRAM_BOT_TOKEN=your_token,CMC_API_KEY=your_api_key,GCP_PROJECT_ID=your_project_id
```

After deployment, set the webhook URL:
```bash
curl -X POST "https://api.telegram.org/bot<YOUR_BOT_TOKEN>/setWebhook?url=<YOUR_FUNCTION_URL>/webhook"
```

### 2. Deploy the Alert Checker Job

```bash
gcloud functions deploy check-price-alerts \
  --gen2 \
  --runtime=go122 \
  --region=us-central1 \
  --source=. \
  --entry-point=CheckPriceAlerts \
  --trigger-topic=price-alerts-trigger \
  --set-env-vars TELEGRAM_BOT_TOKEN=your_token,CMC_API_KEY=your_api_key,GCP_PROJECT_ID=your_project_id
```

### 3. Create a Cloud Scheduler Job

Create a Cloud Scheduler job to trigger the alert checker every 5 minutes:

```bash
# First, create the Pub/Sub topic if it doesn't exist
gcloud pubsub topics create price-alerts-trigger

# Create the scheduler job
gcloud scheduler jobs create pubsub check-alerts-job \
  --schedule="*/5 * * * *" \
  --topic=price-alerts-trigger \
  --message-body='{"action":"check"}' \
  --location=us-central1 \
  --description="Triggers price alert checking every 5 minutes"
```

The schedule `*/5 * * * *` runs the job every 5 minutes.

## Usage

### Available Commands

Users can interact with the bot using these commands:

- `/start` or `/help` - Show help message
- `/setalert <symbol> <price> <above|below>` - Set a price alert
  - Example: `/setalert BTC 50000 above` - Alert when Bitcoin goes above $50,000
  - Example: `/setalert ETH 2000 below` - Alert when Ethereum goes below $2,000

## How It Works

1. **Setting an Alert**: 
   - User sends `/setalert BTC 50000 above` command
   - Bot stores the alert in Firestore
   - User receives confirmation

2. **Checking Alerts**:
   - Every 5 minutes, Cloud Scheduler triggers the alert checker
   - Alert checker fetches all alerts from Firestore
   - For each unique cryptocurrency symbol:
     - Fetches current price from CoinMarketCap
     - Checks if any alert conditions are met
     - Sends Telegram notification to users with triggered alerts

3. **Notifications**:
   - Users receive a message when their alert is triggered
   - Message includes current price and target price
   - Different emojis for "above" (🚀) and "below" (📉) alerts

## Project Structure

```
.
├── function.go                          # Telegram webhook handler entry point
├── alertjob_function.go                 # Alert checker job entry point
├── internal/
│   ├── adapters/
│   │   └── alerts_firestore_repository.go  # Firestore data access
│   ├── alertjob/
│   │   └── alertjob.go                     # Alert job initialization
│   ├── domain/
│   │   └── alerts/
│   │       ├── price_alert.go              # Alert domain model
│   │       └── alert_type.go               # Alert type enum
│   ├── services/
│   │   ├── alert_checker.go                # Alert checking logic
│   │   └── price_service.go                # Price fetching from CoinMarketCap
│   └── telegram/
│       ├── telegram.go                     # Telegram bot initialization
│       └── service/
│           └── service.go                  # Telegram command handlers
```

## Development

### Build

```bash
go build -v ./...
```

### Run Tests

```bash
go test ./...
```

### Local Testing

To test locally, set the required environment variables:

```bash
export TELEGRAM_BOT_TOKEN="your_token"
export CMC_API_KEY="your_api_key"
export GCP_PROJECT_ID="your_project_id"
```

## Monitoring

Monitor your Cloud Functions in the Google Cloud Console:
- View logs: `gcloud functions logs read check-price-alerts --limit 50`
- View metrics in the Cloud Console Functions dashboard

## Cost Considerations

- **Cloud Functions**: Pay per invocation and compute time
- **Cloud Scheduler**: First 3 jobs per month are free
- **Firestore**: Free tier includes 1 GB storage and 50K reads per day
- **CoinMarketCap API**: Free tier includes 333 requests per day (enough for ~33 symbols checked every 5 minutes)

## Troubleshooting

### Alerts not triggering
- Check Cloud Scheduler job is enabled and running
- Verify environment variables are set correctly
- Check Cloud Function logs for errors
- Ensure Firestore has the correct collection name ("alerts")

### Price fetching fails
- Verify CMC_API_KEY is valid
- Check if you've exceeded API rate limits
- Verify cryptocurrency symbol is correct (use uppercase, e.g., "BTC")

## License

MIT
