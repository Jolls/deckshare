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
		{"priority key present, rev absent", []byte(`{"priority":"mixed"}`), RevOrderDue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseRevOrder(c.preset); got != c.want {
				t.Errorf("ParseRevOrder(%s) = %q, want %q", c.preset, got, c.want)
			}
		})
	}
}

func TestParsePriority(t *testing.T) {
	cases := []struct {
		name   string
		preset []byte
		want   Priority
	}{
		{"nil", nil, PriorityDue},
		{"empty object", []byte(`{}`), PriorityDue},
		{"due", []byte(`{"priority":"due"}`), PriorityDue},
		{"new", []byte(`{"priority":"new"}`), PriorityNew},
		{"mixed", []byte(`{"priority":"mixed"}`), PriorityMixed},
		{"unrecognised", []byte(`{"priority":"bogus"}`), PriorityDue},
		{"wrong type", []byte(`{"priority":5}`), PriorityDue},
		{"not json", []byte(`not json`), PriorityDue},
		{"legacy new.mix=beforeReviews, priority absent", []byte(`{"new":{"mix":"beforeReviews"}}`), PriorityNew},
		{"legacy new.mix=afterReviews, priority absent", []byte(`{"new":{"mix":"afterReviews"}}`), PriorityDue},
		{"legacy new.mix=mixed, priority absent", []byte(`{"new":{"mix":"mixed"}}`), PriorityMixed},
		{"legacy new.mix unrecognised, priority absent", []byte(`{"new":{"mix":"bogus"}}`), PriorityDue},
		{"priority present takes precedence over legacy new.mix", []byte(`{"new":{"mix":"beforeReviews"},"priority":"due"}`), PriorityDue},
		{"rev key present, priority and new.mix absent", []byte(`{"rev":{"order":"random"}}`), PriorityDue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParsePriority(c.preset); got != c.want {
				t.Errorf("ParsePriority(%s) = %q, want %q", c.preset, got, c.want)
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

func TestPriorityValid(t *testing.T) {
	valid := []Priority{PriorityDue, PriorityNew, PriorityMixed}
	for _, p := range valid {
		if !p.Valid() {
			t.Errorf("Priority(%q).Valid() = false, want true", p)
		}
	}
	if Priority("bogus").Valid() {
		t.Error(`Priority("bogus").Valid() = true, want false`)
	}
}
