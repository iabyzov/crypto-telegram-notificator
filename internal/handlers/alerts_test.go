package handlers

import (
	"testing"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

func TestIsAlertTriggered(t *testing.T) {
	testCases := []struct {
		name         string
		priceAlert   alerts.PriceAlert
		currentPrice float64
		expected     bool
	}{
		{
			name: "BTC More",
			priceAlert: alerts.PriceAlert{
				Symbol:      "BTC",
				TargetPrice: 100000,
				Type:        alerts.More,
			},
			currentPrice: 100001,
			expected:     true,
		},
		{
			name: "BTC Less",
			priceAlert: alerts.PriceAlert{
				Symbol:      "BTC",
				TargetPrice: 100000,
				Type:        alerts.Less,
			},
			currentPrice: 100001,
			expected:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ac := AlertChecker{}
			res := ac.isAlertTriggered(tc.priceAlert, tc.currentPrice)
			if res != tc.expected {
				t.Errorf("got %v, want %v", res, tc.expected)
			}
		})
	}
}
