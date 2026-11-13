package dynacat

import "testing"

func TestHomeAssistantStateText(t *testing.T) {
	one, zero := 1, 0

	tests := []struct {
		state     string
		unit      string
		precision *int
		want      string
	}{
		{"25.3888888888889", "°C", nil, "25.39 °C"},
		{"18.3333333333333", "°C", nil, "18.33 °C"},
		{"20.0000000001", "°C", nil, "20 °C"},
		{"-3.14159", "°C", nil, "-3.14 °C"},
		{"86.0", "°F", nil, "86.0 °F"},
		{"42.35", "%", nil, "42.35 %"},
		{"3000", "ppm", nil, "3000 ppm"},
		{"25.3888888888889", "°C", &one, "25.4 °C"},
		{"86.0", "°F", &zero, "86 °F"},
		{"3000", "ppm", &one, "3000.0 ppm"},
		{"unavailable", "°C", &one, "Unavailable"},
		{"not_home", "", nil, "Not home"},
		{"1e5", "", nil, "1e5"},
		{"2026-11-13T09:11:21+00:00", "", nil, "2026-11-13T09:11:21+00:00"},
	}

	for _, test := range tests {
		if got := homeAssistantStateText(test.state, test.unit, test.precision); got != test.want {
			t.Errorf("homeAssistantStateText(%q, %q) = %q, want %q", test.state, test.unit, got, test.want)
		}
	}
}
