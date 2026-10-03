#!/bin/bash
# S4b: fan-out beyond the worker pool — 8 triggered alerts (> 5 workers) +
# 2 non-triggered. Every triggered alert must be delivered EXACTLY once
# (one message per user, attempts == 8) and deleted; the run must still
# dedup to ONE batched CMC request; non-triggered alerts must survive.
set -u
EV=/Users/ilya/.no-mistakes/evidence/01M40JYMNB7MTP7T2WRQ6GY1BJ
cd "$(dirname "$0")/.." || exit 1
source e2e_world/driver.sh

BIN=e2e_world/bot
PRODUCT_LOG=$EV/product.log
cleanup() { pkill -f "$BIN" 2>/dev/null; }
trap cleanup EXIT

boot_product() {
  if curl -s -m 2 -o /dev/null "http://127.0.0.1:$APP_PORT/health"; then
    echo "STALE PRODUCT on :$APP_PORT — killing it first"; cleanup; sleep 1
  fi
  env_full "$BIN" >> "$PRODUCT_LOG" 2>&1 &
  local i=0
  while true; do
    if curl -s -m 2 -o /dev/null -w '%{http_code}' "$APP/health" | grep -q 200; then
      echo "product is up on $APP_PORT"; return 0
    fi
    i=$((i+1)); [ "$i" -ge 40 ] && { echo "PRODUCT FAILED TO BOOT"; tail -20 "$PRODUCT_LOG"; return 1; }
    sleep 0.5
  done
}

clear_world
curl -s -m 5 -X POST $PROXY/_control/prices -d '{"BTC":60000,"ETH":3000,"SOL":150}' >/dev/null
# 8 triggered (> maxDeliveryWorkers = 5): 4 BTC (incl. at-or-above boundary), 2 ETH, 2 SOL
for spec in "btc-1:501:BTC:more:100" "btc-2:502:BTC:more:59999.99" "btc-3:503:BTC:more:60000" "btc-4:504:BTC:less:999999" \
            "eth-1:505:ETH:less:5000" "eth-2:506:ETH:less:3000" "sol-1:507:SOL:more:100" "sol-2:508:SOL:less:200"; do
  IFS=: read -r doc user sym typ price <<< "$spec"
  curl -s -m 5 -X POST $PROXY/_control/seed -d "{\"doc_id\":\"s4b-$doc\",\"user_id\":$user,\"coin_id\":\"$sym\",\"target_price\":$price,\"type\":\"$typ\"}" >/dev/null
done
# 2 non-triggered
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4b-hold-1","user_id":509,"coin_id":"BTC","target_price":999999,"type":"more"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4b-hold-2","user_id":510,"coin_id":"ETH","target_price":100,"type":"less"}' >/dev/null

boot_product || exit 1
curl -s -m 60 "$APP/check-alerts"; echo
wait_msgs 8 10
echo "--- telegram evidence (must be 8 attempts / 8 messages, one per user) ---"
ev_telegram
echo "--- cmc evidence (must be exactly 1 batched request for BTC,ETH,SOL) ---"
ev_cmc
echo "--- firestore evidence (only the 2 holds must remain) ---"
ev_fs
N=$(curl -s -m 5 $PROXY/_evidence/telegram | jq '.messages | length')
D=$(curl -s -m 5 $PROXY/_evidence/telegram | jq '[.messages[].chat_id] | (length == (map(tonumber) | unique | length))')
A=$(curl -s -m 5 $PROXY/_evidence/telegram | jq '.attempts')
G=$(curl -s -m 5 $PROXY/_evidence/telegram | jq '.getMe_calls')
echo "messages=$N distinct_chat_ids=$D attempts=$A (expect 8 sendMessage + $G boot getMe)"
if [ "$N" = "8" ] && [ "$D" = "true" ] && [ "$A" = "$((8 + G))" ]; then
  echo "S4b RESULT: PASS (8 triggered delivered exactly once through the 5-worker pool)"
else
  echo "S4b RESULT: FAIL"
fi