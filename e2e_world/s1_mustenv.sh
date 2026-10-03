#!/bin/bash
# S1: mustEnv contract — the real binary refuses to boot when a required env
# var is missing, with the exact fatal message, before doing anything else.
set -u
EV=/Users/ilya/.no-mistakes/evidence/01M40JYMNB7MTP7T2WRQ6GY1BJ
W=$EV/world.json
PROXY=$(jq -r .proxy_url $W)
FSHOST=$(jq -r .firestore $W)
REDIS=$(jq -r .redis_url $W)
CA=$(jq -r .ca_pem $W)
TOKEN=$(jq -r .bot_token $W)
BIN=e2e_world/bot

run_env() { # runs the binary with one required env var truly unset
  local unset_var="$1"; shift
  # `env -u VAR` is useless here because the same command line re-sets
  # every variable; instead build the full env and drop only the target.
  local -a e=( TELEGRAM_BOT_TOKEN="$TOKEN" CMC_API_KEY="e2e-cmc-key" GCP_PROJECT_ID="no-mistakes-e2e" UPSTASH_REDIS_URL="$REDIS" FIRESTORE_EMULATOR_HOST="$FSHOST" HTTPS_PROXY="$PROXY" SSL_CERT_FILE="$CA" PORT=8091 )
  local -a keep=() kv
  for kv in "${e[@]}"; do
    [ "${kv%%=*}" = "$unset_var" ] && continue
    keep+=("$kv")
  done
  ( env -i GODEBUG=x509sslcertoverrideplatform=1 "${keep[@]}" "$BIN" 2>&1 ) | head -5
  echo "exit_status=${PIPESTATUS[0]}"
}

echo "=== S1: mustEnv — binary refuses to start when each required env var is missing ==="
for var in TELEGRAM_BOT_TOKEN CMC_API_KEY GCP_PROJECT_ID UPSTASH_REDIS_URL; do
  echo "--- run with $var unset ---"
  run_env "$var"
done