# Crypto Telegram Notificator

A Telegram bot that monitors cryptocurrency prices and sends alerts when price targets are met.

## Features

- Set price alerts for cryptocurrencies (above/below target prices)
- Automated price monitoring via scheduled Cloud Function
- Real-time notifications via Telegram
- Data persistence using Google Cloud Firestore

## Architecture

The application is deployed as a Google Cloud Run service with two HTTP endpoints:

1. **Telegram Webhook Handler** (`/webhook`): Handles incoming messages from users via Telegram webhook
2. **Alert Checker Job** (`/check-alerts`): Scheduled job that checks price alerts every 5 minutes

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

### 1. Deploy to Google Cloud Run

Build and deploy the service using Cloud Build:

```bash
gcloud run deploy crypto-telegram-notificator \
  --source=. \
  --region=us-central1 \
  --allow-unauthenticated \
  --set-env-vars TELEGRAM_BOT_TOKEN=your_token,CMC_API_KEY=your_api_key,GCP_PROJECT_ID=your_project_id
```

Or using Docker:

```bash
# Build the Docker image
docker build -t gcr.io/YOUR_PROJECT_ID/crypto-telegram-notificator .

# Push to Google Container Registry
docker push gcr.io/YOUR_PROJECT_ID/crypto-telegram-notificator

# Deploy to Cloud Run
gcloud run deploy crypto-telegram-notificator \
  --image=gcr.io/YOUR_PROJECT_ID/crypto-telegram-notificator \
  --region=us-central1 \
  --allow-unauthenticated \
  --set-env-vars TELEGRAM_BOT_TOKEN=your_token,CMC_API_KEY=your_api_key,GCP_PROJECT_ID=your_project_id
```

After deployment, note the service URL (e.g., `https://crypto-telegram-notificator-xxxxx.run.app`).

### 2. Set the Telegram Webhook

Configure Telegram to send updates to your Cloud Run service:

```bash
curl -X POST "https://api.telegram.org/bot<YOUR_BOT_TOKEN>/setWebhook?url=<YOUR_CLOUD_RUN_URL>/webhook"
```

### 3. Create a Cloud Scheduler Job

Create a Cloud Scheduler job to trigger the alert checker every 5 minutes:

```bash
# Create the scheduler job to call the HTTP endpoint
gcloud scheduler jobs create http check-alerts-job \
  --schedule="*/5 * * * *" \
  --uri="<YOUR_CLOUD_RUN_URL>/check-alerts" \
  --http-method=GET \
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
- `/listalerts` - View all your active alerts with their IDs
- `/deletealert <alert_id>` - Remove a specific alert using its ID
  - Example: `/deletealert abc123def456` - Delete the alert with ID abc123def456

## How It Works

1. **Setting an Alert**: 
   - User sends `/setalert BTC 50000 above` command
   - Bot stores the alert in Firestore
   - User receives confirmation with alert details

2. **Managing Alerts**:
   - User can view all their active alerts with `/listalerts`
   - Each alert is displayed with a unique ID, symbol, target price, and type (above/below)
   - User can delete unwanted alerts using `/deletealert <alert_id>`
   - The system ensures users can only view and delete their own alerts

3. **Checking Alerts**:
   - Every 5 minutes, Cloud Scheduler triggers the `/check-alerts` endpoint
   - Alert checker fetches all alerts from Firestore
   - For each unique cryptocurrency symbol:
     - Fetches current price from CoinMarketCap
     - Checks if any alert conditions are met
     - Sends Telegram notification to users with triggered alerts
     - Automatically deletes triggered alerts

4. **Notifications**:
   - Users receive a message when their alert is triggered
   - Message includes current price and target price
   - Different emojis for "above" (🚀) and "below" (📉) alerts

## Project Structure

```
.
├── main.go                              # Cloud Run HTTP server entry point
├── Dockerfile                           # Docker configuration for Cloud Run
├── internal/
│   ├── adapters/
│   │   └── alerts_firestore_repository.go  # Firestore data access
│   ├── domain/
│   │   └── alerts/
│   │       ├── price_alert.go              # Alert domain model
│   │       └── alert_type.go               # Alert type enum
│   ├── handlers/
│   │   ├── alerts.go                       # Alert checking logic
│   │   └── telegram.go                     # Telegram webhook and command handlers
│   └── services/
│       └── price_service.go                # Price fetching from CoinMarketCap
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

Monitor your Cloud Run service in the Google Cloud Console:
- View logs: `gcloud logging read "resource.type=cloud_run_revision AND resource.labels.service_name=crypto-telegram-notificator" --limit 50`
- View metrics in the Cloud Console Cloud Run dashboard

## Cost Considerations

- **Cloud Run**: Free tier includes 2 million requests per month, plus always-free CPU/memory allocation
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
