package review

import "testing"

func TestNewPerDay(t *testing.T) {
	cases := []struct {
		name   string
		preset []byte
		want   int32
	}{
		{"nil", nil, DefaultNewPerDay},
		{"empty object", []byte(`{}`), DefaultNewPerDay},
		{"new present, perDay absent", []byte(`{"new":{}}`), DefaultNewPerDay},
		{"zero", []byte(`{"new":{"perDay":0}}`), 0},
		{"five", []byte(`{"new":{"perDay":5}}`), 5},
		{"negative", []byte(`{"new":{"perDay":-1}}`), DefaultNewPerDay},
		{"over max", []byte(`{"new":{"perDay":10000}}`), DefaultNewPerDay},
		{"wrong type", []byte(`{"new":{"perDay":"20"}}`), DefaultNewPerDay},
		{"not json", []byte(`not json`), DefaultNewPerDay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NewPerDay(c.preset); got != c.want {
				t.Errorf("NewPerDay(%s) = %d, want %d", c.preset, got, c.want)
			}
		})
	}
}

func TestNewRemaining(t *testing.T) {
	cases := []struct {
		name            string
		perDay          int32
		introducedToday int64
		want            int32
	}{
		{"none introduced", 20, 0, 20},
		{"partial", 20, 12, 8},
		{"exactly at limit", 20, 20, 0},
		{"over limit", 20, 25, 0},
		{"zero perDay", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NewRemaining(c.perDay, c.introducedToday); got != c.want {
				t.Errorf("NewRemaining(%d, %d) = %d, want %d", c.perDay, c.introducedToday, got, c.want)
			}
		})
	}
}
