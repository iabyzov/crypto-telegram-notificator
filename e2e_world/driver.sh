#!/bin/bash
# Live driver: drives the REAL compiled binary of crypto-telegram-notificator
# against the disposable e2e world (fake Telegram via MITM proxy, fake CMC,
# fake Firestore gRPC, miniredis). Writes evidence to $EV.
set -u
EV=/Users/ilya/.no-mistakes/evidence/01M40JYMNB7MTP7T2WRQ6GY1BJ
W=$EV/world.json
PROXY=$(jq -r .proxy_url $W)
FSHOST=$(jq -r .firestore $W)
REDIS=$(jq -r .redis_url $W)
CA=$(jq -r .ca_pem $W)
TOKEN=$(jq -r .bot_token $W)
APP_PORT=8090
APP=http://127.0.0.1:$APP_PORT
SECRET="e2e-s3cret"

env_full() {
  TELEGRAM_BOT_TOKEN="$TOKEN" \
  CMC_API_KEY="e2e-cmc-key" \
  GCP_PROJECT_ID="no-mistakes-e2e" \
  UPSTASH_REDIS_URL="$REDIS" \
  FIRESTORE_EMULATOR_HOST="$FSHOST" \
  PORT=$APP_PORT \
  TELEGRAM_WEBHOOK_SECRET="$SECRET" \
  LLM_API_KEY="e2e-llm-key" \
  LLM_BASE_URL="http://127.0.0.1:${PROXY##*:}/v1" \
  LLM_MODEL="e2e-model" \
  GODEBUG="x509sslcertoverrideplatform=1" \
  HTTPS_PROXY="$PROXY" \
  SSL_CERT_FILE="$CA" "$@"
}

log() { printf '\n=== %s ===\n' "$*"; }
run() { printf '\n$ %s\n' "$*"; "$@" 2>&1; }

post_cmd() { # chat_id text [update_id]
  local chat=$1 text=$2 uid=${3:-$((RANDOM*1000))}
  curl -s -o /tmp/wh_resp -w '%{http_code}' -X POST "$APP/webhook" \
    -H "X-Telegram-Bot-Api-Secret-Token: $SECRET" \
    -H 'Content-Type: application/json' \
    -d "{\"update_id\":$uid,\"message\":{\"message_id\":$uid,\"from\":{\"id\":$chat,\"is_bot\":false,\"first_name\":\"E2E User\"},\"chat\":{\"id\":$chat,\"type\":\"private\",\"first_name\":\"E2E User\"},\"date\":1700000000,\"text\":\"$text\",\"entities\":[{\"offset\":0,\"length\":$(echo "$text" | awk '{print length($1)}'),\"type\":\"bot_command\"}]}}"
}
wait_msgs() { # expected_count [timeout_s]
  local want=${1:-1} t=${2:-10} waited=0
  while true; do
    local n=$(curl -s $PROXY/_evidence/telegram | jq '.messages | length')
    if [ "${n:-0}" -ge "$want" ]; then echo "messages=$n (>= $want) after ${waited}s"; return 0; fi
    if [ "$waited" -ge "$t" ]; then echo "TIMEOUT waiting for $want messages (have $n)"; return 1; fi
    sleep 0.3; waited=$((waited+1))
  done
}
ev_telegram() { curl -s $PROXY/_evidence/telegram | jq .; }
ev_cmc()      { curl -s $PROXY/_evidence/cmc | jq .; }
ev_fs()       { curl -s $PROXY/_evidence/firestore | jq .; }
ev_redis()    { curl -s $PROXY/_evidence/redis | jq .; }
ev_llm()      { curl -s $PROXY/_evidence/llm | jq .; }
clear_world() { curl -s -X POST $PROXY/_control/clear >/dev/null; echo "world cleared"; }
last_msg()    { curl -s $PROXY/_evidence/telegram | jq -r '.messages[-1].text'; }
msg_texts()   { curl -s $PROXY/_evidence/telegram | jq -r '.messages[].text'; }