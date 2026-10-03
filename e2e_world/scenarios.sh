#!/bin/bash
# Live scenarios S2-S6: drive the REAL compiled binary of
# crypto-telegram-notificator end to end against the disposable e2e world
# (fake Telegram via MITM proxy, fake CMC, fake Firestore gRPC, miniredis,
# fake OpenAI-compatible LLM). Transcripts are teed to $EV.
set -u
EV=/Users/ilya/.no-mistakes/evidence/01M40JYMNB7MTP7T2WRQ6GY1BJ
cd "$(dirname "$0")/.." || exit 1
source e2e_world/driver.sh

BIN=e2e_world/bot
PRODUCT_LOG=$EV/product.log

cleanup() {
  # Kill the binary itself: the backgrounded env_full runs as a subshell, so
  # killing its PID would orphan the bot process holding :8090/:8080.
  pkill -f "$BIN" 2>/dev/null
}
trap cleanup EXIT

boot_product() {
  # Refuse to drive a stale product left over from an earlier run.
  if curl -s -m 2 -o /dev/null http://127.0.0.1:$APP_PORT/health; then
    echo "STALE PRODUCT on :$APP_PORT — killing it first"
    cleanup; sleep 1
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

metrics() { curl -s -m 5 http://127.0.0.1:8080/metrics; }
metric()  { metrics | grep -E "^$1" | head -1; }

echo "product.log: $PRODUCT_LOG"

# =========================================================================
log "S2: binary boots against the world and serves its HTTP surface"
# =========================================================================
rm -f "$PRODUCT_LOG"
boot_product || exit 1
run curl -s -m 5 "$APP/health"
run curl -s -m 5 -o /dev/null -w 'pprof_status=%{http_code}\n' "$APP/debug/pprof/"
echo "--- metrics counters present on :8080 ---"
metric 'webhook_requests_total'
metric 'check_alert_requests_total'
metric 'telegram_notification_errors_total'

# =========================================================================
log "S3: webhook command flow through the real binary"
# =========================================================================
run curl -s -m 5 -o /dev/null -w 'no_secret=%{http_code}\n' -X POST "$APP/webhook" -H 'Content-Type: application/json' -d '{"update_id":1}'
run curl -s -m 5 -o /dev/null -w 'wrong_secret=%{http_code}\n' -X POST "$APP/webhook" -H 'X-Telegram-Bot-Api-Secret-Token: nope' -H 'Content-Type: application/json' -d '{"update_id":1}'

clear_world
post_cmd 4242 "/start";        wait_msgs 1
post_cmd 4242 "/help";         wait_msgs 2
last_msg

post_cmd 4242 "/setalert BTC 50000 more"; wait_msgs 3
echo "--- firestore after /setalert ---"; ev_fs
post_cmd 4242 "/setalert ETH 4000 less"; wait_msgs 4

post_cmd 4242 "/listalerts"; wait_msgs 5
ALERT_ID=$(msg_texts | grep -o 'ID: [A-Za-z0-9_-]*' | head -1 | awk '{print $2}')
echo "extracted alert id: $ALERT_ID"

echo "--- foreign user 5555 tries to delete 4242's alert (must be refused) ---"
post_cmd 5555 "/deletealert $ALERT_ID"; wait_msgs 6
last_msg
echo "--- firestore must still hold both alerts ---"; ev_fs

post_cmd 4242 "/deletealert $ALERT_ID"; wait_msgs 7
last_msg
echo "--- firestore after owner delete (one alert must remain) ---"; ev_fs

post_cmd 4242 "/deletealert nonexistent-id"; wait_msgs 8
last_msg

post_cmd 4242 "/alert I want an alert for Bitcoin when it rises above 100k"; wait_msgs 9
last_msg
echo "--- llm requests seen by the fake ---"; ev_llm

# =========================================================================
log "S4: /check-alerts triggers a burst: dedup to ONE CMC call, exactly-once fan-out delivery"
# =========================================================================
clear_world
curl -s -m 5 -X POST $PROXY/_control/prices -d '{"BTC":60000,"ETH":3000,"SOL":150}' >/dev/null
# 7 triggered alerts across 3 symbols (4 BTC, 2 ETH, 1 SOL) > 5-worker pool, plus 1 non-triggered
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-btc-1","user_id":101,"coin_id":"BTC","target_price":100,"type":"more"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-btc-2","user_id":102,"coin_id":"BTC","target_price":59999.99,"type":"more"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-btc-3","user_id":103,"coin_id":"BTC","target_price":200,"type":"less"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-btc-4","user_id":104,"coin_id":"BTC","target_price":60000,"type":"more"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-eth-1","user_id":105,"coin_id":"ETH","target_price":5000,"type":"less"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-eth-2","user_id":106,"coin_id":"ETH","target_price":2999.99,"type":"less"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-sol-1","user_id":107,"coin_id":"SOL","target_price":100,"type":"more"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s4-hold-1","user_id":108,"coin_id":"BTC","target_price":999999,"type":"more"}' >/dev/null
run curl -s -m 60 "$APP/check-alerts"
wait_msgs 7 15
echo "--- telegram evidence (7 notifications, one per user) ---"; ev_telegram
echo "--- cmc evidence (must be exactly 1 batched request) ---"; ev_cmc
echo "--- firestore evidence (only s4-hold-1 must remain) ---"; ev_fs

# =========================================================================
log "S5 (adversarial): telegram outage -> stamps + keep; recovery -> redeliver; stale stamp -> dead-letter"
# =========================================================================
clear_world
curl -s -m 5 -X POST $PROXY/_control/prices -d '{"BTC":60000,"ETH":3000}' >/dev/null
NOW_MS=$(($(date +%s) * 1000))
STALE_MS=$((NOW_MS - 7200 * 1000))
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s5-a1","user_id":301,"coin_id":"BTC","target_price":100,"type":"more"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s5-a2","user_id":302,"coin_id":"ETH","target_price":5000,"type":"less"}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d "{\"doc_id\":\"s5-dl\",\"user_id\":303,\"coin_id\":\"BTC\",\"target_price\":100,\"type\":\"more\",\"delivery_failed_at_ms\":$STALE_MS}" >/dev/null
echo "--- force telegram outage ---"
curl -s -m 5 -X POST $PROXY/_control/telegram -d '{"fail":true}'; echo
run curl -s -m 90 "$APP/check-alerts"
echo "--- telegram attempts during outage (8 = 2 alerts x 4 tries; DL alert must NOT be attempted) ---"; ev_telegram
echo "--- firestore: s5-a1/s5-a2 kept + stamped, s5-dl dead-lettered (gone) ---"; ev_fs
echo "--- product log: dead-letter ERROR line ---"
grep -i 'dead-letter' "$PRODUCT_LOG" | tail -2
echo "--- metrics: notification errors grew ---"; metric 'telegram_notification_errors_total'

echo "--- telegram recovers; next run must redeliver both ---"
curl -s -m 5 -X POST $PROXY/_control/telegram -d '{"fail":false}' >/dev/null
run curl -s -m 60 "$APP/check-alerts"
wait_msgs 2 10
echo "--- telegram evidence now ---"; ev_telegram
echo "--- firestore: all delivered alerts deleted ---"; ev_fs

# =========================================================================
log "S6: redis cache: second run within TTL serves prices from cache (no 2nd CMC call)"
# =========================================================================
clear_world
curl -s -m 5 -X POST $PROXY/_control/prices -d '{"BTC":60000}' >/dev/null
curl -s -m 5 -X POST $PROXY/_control/seed -d '{"doc_id":"s6-hold","user_id":401,"coin_id":"BTC","target_price":999999,"type":"more"}' >/dev/null
run curl -s -m 30 "$APP/check-alerts"
CMC1=$(curl -s -m 5 $PROXY/_evidence/cmc | jq .count)
echo "cmc requests after run 1: $CMC1"
echo "--- redis evidence after run 1 (cache populated) ---"; ev_redis
run curl -s -m 30 "$APP/check-alerts"
CMC2=$(curl -s -m 5 $PROXY/_evidence/cmc | jq .count)
echo "cmc requests after run 2: $CMC2 (must equal run-1 count: cache hit, no new CMC call)"
[ "$CMC1" = "$CMC2" ] && echo "S6 RESULT: PASS (no additional CMC request)" || echo "S6 RESULT: FAIL"

log "done — killing product"