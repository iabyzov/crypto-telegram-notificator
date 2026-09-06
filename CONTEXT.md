# Crypto Telegram Notificator

A Telegram bot that monitors cryptocurrency prices (CoinMarketCap) and notifies users when their price alerts trigger.

## Language

**PriceAlert**:
A user's registered condition: a symbol, a target price, and an AlertType. Fires once, then is deleted.
_Avoid_: watch, rule, alert rule

**AlertType**:
The AlertType of a PriceAlert. Exactly two canonical values: More (fires when the price rises to or above the target) and Less (fires when the price drops to or below the target). One vocabulary across commands, storage, and messages.
_Avoid_: direction, above, below

**Triggered**:
An alert whose condition is satisfied by the current price, inclusive at the target (More: `>=`, Less: `<=`).
_Avoid_: fired, hit, matched