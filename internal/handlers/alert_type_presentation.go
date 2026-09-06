package handlers

import "github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"

// alertTypePresentation carries the display fragments for an AlertType: the
// emoji and the trigger phrase used in bot messages. Both the triggered
// notification and the alert list consume this single mapping, so the
// fragments cannot drift apart. The default case mirrors the storage
// mapping's default: an unrecognized type renders as More.
type alertTypePresentation struct {
	emoji         string
	triggerPhrase string
}

func alertTypePresentationFor(t alerts.AlertType) alertTypePresentation {
	switch t {
	case alerts.Less:
		return alertTypePresentation{
			emoji:         "📉",
			triggerPhrase: "reached or dropped below your target",
		}
	default:
		return alertTypePresentation{
			emoji:         "🚀",
			triggerPhrase: "reached or exceeded your target",
		}
	}
}
