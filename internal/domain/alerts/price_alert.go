package alerts

import "time"

type PriceAlert struct {
	Id          string
	Symbol      string
	TargetPrice float64
	UserID      int64
	Type        AlertType
	// DeliveryFailedAt records when notification delivery FIRST failed: the
	// earliest stamp is kept, and later failing runs never overwrite it, so
	// the one-hour dead-letter deadline measures from the first failure.
	// Zero means the alert has not failed delivery (yet); a non-zero value
	// marks an alert awaiting redelivery on the next scheduled check.
	DeliveryFailedAt time.Time
}

// IsTriggeredBy reports whether the current price satisfies the alert's
// condition. The comparison is inclusive at the target: More fires at or
// above, Less at or below. An invalid alert type never triggers.
func (a PriceAlert) IsTriggeredBy(currentPrice float64) bool {
	switch a.Type {
	case More:
		return currentPrice >= a.TargetPrice
	case Less:
		return currentPrice <= a.TargetPrice
	default:
		return false
	}
}
