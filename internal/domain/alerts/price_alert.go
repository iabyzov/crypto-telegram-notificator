package alerts

type PriceAlert struct {
	Id          string
	Symbol      string
	TargetPrice float64
	UserID      int64
	Type        AlertType
}
