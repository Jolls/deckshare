package review

import "testing"

func TestParseRevOrder(t *testing.T) {
	cases := []struct {
		name   string
		preset []byte
		want   RevOrder
	}{
		{"nil", nil, RevOrderDue},
		{"empty object", []byte(`{}`), RevOrderDue},
		{"rev present, order absent", []byte(`{"rev":{}}`), RevOrderDue},
		{"due", []byte(`{"rev":{"order":"due"}}`), RevOrderDue},
		{"random", []byte(`{"rev":{"order":"random"}}`), RevOrderRandom},
		{"intervalAsc", []byte(`{"rev":{"order":"intervalAsc"}}`), RevOrderIntervalAsc},
		{"intervalDesc", []byte(`{"rev":{"order":"intervalDesc"}}`), RevOrderIntervalDesc},
		{"unrecognised", []byte(`{"rev":{"order":"bogus"}}`), RevOrderDue},
		{"wrong type", []byte(`{"rev":{"order":5}}`), RevOrderDue},
		{"not json", []byte(`not json`), RevOrderDue},
		{"new key present, rev absent", []byte(`{"new":{"mix":"mixed"}}`), RevOrderDue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseRevOrder(c.preset); got != c.want {
				t.Errorf("ParseRevOrder(%s) = %q, want %q", c.preset, got, c.want)
			}
		})
	}
}

func TestParseNewMix(t *testing.T) {
	cases := []struct {
		name   string
		preset []byte
		want   NewMix
	}{
		{"nil", nil, NewMixAfterReviews},
		{"empty object", []byte(`{}`), NewMixAfterReviews},
		{"new present, mix absent", []byte(`{"new":{}}`), NewMixAfterReviews},
		{"afterReviews", []byte(`{"new":{"mix":"afterReviews"}}`), NewMixAfterReviews},
		{"beforeReviews", []byte(`{"new":{"mix":"beforeReviews"}}`), NewMixBeforeReviews},
		{"mixed", []byte(`{"new":{"mix":"mixed"}}`), NewMixMixed},
		{"unrecognised", []byte(`{"new":{"mix":"bogus"}}`), NewMixAfterReviews},
		{"wrong type", []byte(`{"new":{"mix":5}}`), NewMixAfterReviews},
		{"not json", []byte(`not json`), NewMixAfterReviews},
		{"rev key present, new absent", []byte(`{"rev":{"order":"random"}}`), NewMixAfterReviews},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseNewMix(c.preset); got != c.want {
				t.Errorf("ParseNewMix(%s) = %q, want %q", c.preset, got, c.want)
			}
		})
	}
}

func TestRevOrderValid(t *testing.T) {
	valid := []RevOrder{RevOrderDue, RevOrderRandom, RevOrderIntervalAsc, RevOrderIntervalDesc}
	for _, o := range valid {
		if !o.Valid() {
			t.Errorf("RevOrder(%q).Valid() = false, want true", o)
		}
	}
	if RevOrder("bogus").Valid() {
		t.Error(`RevOrder("bogus").Valid() = true, want false`)
	}
}

func TestNewMixValid(t *testing.T) {
	valid := []NewMix{NewMixAfterReviews, NewMixBeforeReviews, NewMixMixed}
	for _, m := range valid {
		if !m.Valid() {
			t.Errorf("NewMix(%q).Valid() = false, want true", m)
		}
	}
	if NewMix("bogus").Valid() {
		t.Error(`NewMix("bogus").Valid() = true, want false`)
	}
}
