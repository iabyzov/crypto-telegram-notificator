# ADR-0001: At-least-once alert notification delivery

## Status

Accepted (2026-09-29, implementation of issues #22 and #29; decision originally resolved during the #11 grilling session)

## Context

The alert checker originally deleted a triggered alert from Firestore and only
then attempted the Telegram notification. A failed send (Telegram outage,
rate limit, transient 5xx) meant the alert was already gone: the user's alert
was silently lost with no way to retry. The bot promised to notify users about
price triggers but delivered them on a best-effort basis with silent loss.

## Decision

Notification delivery is at-least-once, send-first:

1. **Send first.** For each triggered alert, the checker attempts the
   Telegram notification before touching storage. The alert is deleted only
   after a send succeeds.
2. **Retry with exponential backoff.** A failed send is retried with waits of
   2s, 4s, and 8s - four attempts total per checker run (the first attempt is
   immediate). Success on any attempt delivers and then deletes.
3. **Mark and keep on total failure.** If all attempts fail within the run,
   the alert stays in storage, stamped with `delivery_failed_at` (Unix
   milliseconds, `omitempty` on the Firestore model) via
   `AlertsRepository.MarkDeliveryFailed`. The next scheduled run (every 5
   minutes) picks it up and retries delivery. The stamp records the FIRST
   failure: an alert already carrying a stamp keeps its earliest one, so the
   dead-letter deadline measures from the first failure, not the most
   recent one.
4. **Dead-letter after one hour** (issue #29): before any pricing or
   delivery, each run sweeps the fetched alerts for ones whose delivery has
   been failing for over an hour, keyed on age alone (whether or not the
   alert still triggers - a retraced alert that lingers is dead-lettered
   too, so failures never linger invisibly). A swept alert is terminally
   deleted, incremented into `telegram_notification_errors_total`, and
   reported via an slog ERROR with its details, so the loss is observable
   (counter + log) instead of silent. The sweep runs before price fetching
   so dead alerts never spend CoinMarketCap quota. If the terminal delete
   itself fails, the alert stays and the next run's sweep retries it.

   The one-hour window and the first-failure stamp make the deadline
   self-limiting: an alert can fail at most ~12 scheduler runs before it is
   terminally removed, bounded by the injected clock in tests and the wall
   clock in production.

The retry schedule and failure timestamps run on an injected clock
(`checkerClock`), so tests observe the exact backoff without real sleeps and
dead-lettering (#29) can control the 1-hour window deterministically.

## Consequences

- A Telegram outage no longer loses alerts: undelivered alerts accumulate and
  are re-attempted every scheduler run.
- Duplicate notifications are now possible (at-least-once, not exactly-once):
  if a send succeeds but the subsequent Firestore delete fails, the next run
  re-sends. Users may see a repeated message; that is the accepted trade-off
  against silent loss.
- A total-failure run takes up to ~14s per triggered alert (2+4+8s waits plus
  request time); the 5-minute scheduler interval absorbs this at current
  alert volumes.
- Dead-lettering trades redelivery for observability: an alert failing for
  over an hour is removed without the user ever receiving it. The user is
  not notified of the loss; operators are (counter + ERROR log). That is
  the accepted alternative to unbounded retry queues on a serverless
  scheduler.
- `telegram_notification_errors_total` increments per failed attempt and once
  more per dead-lettered alert, so Cloud Monitoring can alarm on both
  transient blips and terminal losses (ticket #33's counter contract).
- `AlertsRepository` grows `MarkDeliveryFailed`; every implementation
  (Firestore, test fakes) must persist the timestamp in place, without
  rewriting the rest of the document (a Firestore field `Update`, which
  returns `NotFound` instead of recreating a concurrently deleted document).
- Old Firestore documents without `delivery_failed_at` keep loading: the field
  is `omitempty` and maps zero to "never failed".
