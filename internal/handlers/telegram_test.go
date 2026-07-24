package handlers

import (
	"testing"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
)

func TestParseAlertType(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    alerts.AlertType
		wantErr bool
	}{
		{name: "more", input: "More", want: alerts.More},
		{name: "less", input: "Less", want: alerts.Less},
		{name: "case-insensitive upper", input: "MORE", want: alerts.More},
		{name: "case-insensitive lower", input: "less", want: alerts.Less},
		{name: "mixed case", input: "LeSs", want: alerts.Less},
		{name: "empty string", input: "", wantErr: true},
		{name: "invalid word", input: "banana", wantErr: true},
		{name: "above synonym", input: "above", wantErr: true},
		{name: "below synonym", input: "below", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAlertType(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseAlertType want err, gets nil")
				}
			} else {
				if err != nil {
					t.Fatalf("ParseAlertType no want err, gets %v", err)
				} else if tt.want != got {
					t.Errorf("ParseAlertType want %s, got %s", tt.want, got)
				}
			}
		})
	}
}
