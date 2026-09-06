package alerts

import "testing"

func TestParseAlertType(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    AlertType
		wantErr bool
	}{
		{name: "more", input: "More", want: More},
		{name: "less", input: "Less", want: Less},
		{name: "case-insensitive upper", input: "MORE", want: More},
		{name: "case-insensitive lower", input: "less", want: Less},
		{name: "mixed case", input: "LeSs", want: Less},
		{name: "empty string", input: "", wantErr: true},
		{name: "invalid word", input: "banana", wantErr: true},
		{name: "above synonym rejected", input: "above", wantErr: true},
		{name: "below synonym rejected", input: "below", wantErr: true},
		{name: "padded word rejected", input: " more", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAlertType(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseAlertType(%q) want err, got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Fatalf("ParseAlertType(%q) no want err, got %v", tt.input, err)
				}
				if tt.want != got {
					t.Errorf("ParseAlertType(%q) want %s, got %s", tt.input, tt.want, got)
				}
			}
		})
	}
}

func TestIsTriggeredBy(t *testing.T) {
	tests := []struct {
		name      string
		alertType AlertType
		target    float64
		price     float64
		want      bool
	}{
		// More fires at or above the target (inclusive boundary).
		{name: "More at target", alertType: More, target: 100, price: 100, want: true},
		{name: "More above target", alertType: More, target: 100, price: 100.01, want: true},
		{name: "More below target", alertType: More, target: 100, price: 99.99, want: false},
		// Less fires at or below the target (inclusive boundary).
		{name: "Less at target", alertType: Less, target: 100, price: 100, want: true},
		{name: "Less below target", alertType: Less, target: 100, price: 99.99, want: true},
		{name: "Less above target", alertType: Less, target: 100, price: 100.01, want: false},
		// An invalid type never triggers.
		{name: "invalid type", alertType: AlertType(42), target: 100, price: 100, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alert := PriceAlert{Symbol: "BTC", TargetPrice: tt.target, Type: tt.alertType}
			if got := alert.IsTriggeredBy(tt.price); got != tt.want {
				t.Errorf("IsTriggeredBy(price=%v) for %s alert with target %v: want %v, got %v",
					tt.price, tt.alertType, tt.target, tt.want, got)
			}
		})
	}
}
