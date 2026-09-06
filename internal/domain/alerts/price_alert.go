package alerts

type PriceAlert struct {
	Id          string
	Symbol      string
	TargetPrice float64
	UserID      int64
	Type        AlertType
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
