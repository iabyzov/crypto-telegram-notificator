package alerts

import (
	"fmt"
	"strings"
)

//go:generate stringer -type=AlertType
type AlertType int

const (
	More AlertType = iota
	Less
)

// ParseAlertType converts a user- or storage-supplied string into an AlertType.
// It is a case-insensitive exact match against the canonical words More/Less;
// anything else — including the non-canonical synonyms "above"/"below" — is an error.
func ParseAlertType(s string) (AlertType, error) {
	for i := More; i <= Less; i++ {
		if strings.EqualFold(s, i.String()) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("invalid alert type %q: expected More or Less", s)
}
