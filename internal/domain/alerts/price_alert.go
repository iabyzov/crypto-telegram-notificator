package alerts

type PriceAlert struct {
	Symbol      string
	TargetPrice float64
	UserID      int64
	Type        AlertType
}
