package alerts

//go:generate stringer -type=AlertType
type AlertType int

const (
	More AlertType = iota
	Less
)
