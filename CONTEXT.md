# Crypto Telegram Notificator

A Telegram bot that monitors cryptocurrency prices (CoinMarketCap) and notifies users when their price alerts trigger.

## Language

**PriceAlert**:
A user's registered condition: a symbol, a target price, and an AlertType. One-shot: fires when triggered, then is deleted once Delivery succeeds.
_Avoid_: watch, rule, alert rule

**AlertType**:
The AlertType of a PriceAlert. Exactly two canonical values: More (fires when the price rises to or above the target) and Less (fires when the price drops to or below the target). One vocabulary across commands, storage, and messages.
_Avoid_: direction, above, below

**Triggered**:
An alert whose condition is satisfied by the current price, inclusive at the target (More: `>=`, Less: `<=`).
_Avoid_: fired, hit, matched

**Delivery**:
Getting a Triggered alert's notification to its user via Telegram. At-least-once: sent before the alert is deleted, retried with 2s/4s/8s backoff; total failure stamps `delivery_failed_at` (the first failure - kept, not overwritten) and keeps the alert for the next scheduled run.
_Avoid_: notification, message, fire-and-forget

**DeadLetter**:
Terminal removal of a Delivery-failed alert after 1 hour of failure (from the first failure): delete from storage + `telegram_notification_errors_total` + error log. The alert's notification is never delivered; the loss is observable to operators only.
_Avoid_: purge, drop, expire
