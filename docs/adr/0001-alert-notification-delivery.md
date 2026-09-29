# ADR-0001: At-least-once alert notification delivery

## Status

Accepted (2026-09-29, implementation of issue #22; decision originally resolved during the #11 grilling session)

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
   minutes) picks it up and retries delivery.
4. **Dead-lettering is a separate decision** (issue #29): alerts that keep
   failing for more than an hour will be deleted with an operator-visible
   signal (counter + structured log). This ADR covers only the retry-and-keep
   loop and the timestamp seam that dead-lettering reads.

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
- `AlertsRepository` grows `MarkDeliveryFailed`; every implementation
  (Firestore, test fakes) must persist the timestamp in place, without
  rewriting the rest of the document (a Firestore field `Update`, which
  returns `NotFound` instead of recreating a concurrently deleted document).
- Old Firestore documents without `delivery_failed_at` keep loading: the field
  is `omitempty` and maps zero to "never failed".
