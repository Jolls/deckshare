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

func TestRevPerDay(t *testing.T) {
	cases := []struct {
		name   string
		preset []byte
		want   int32
	}{
		{"nil", nil, DefaultRevPerDay},
		{"empty object", []byte(`{}`), DefaultRevPerDay},
		{"rev present, perDay absent", []byte(`{"rev":{}}`), DefaultRevPerDay},
		{"zero", []byte(`{"rev":{"perDay":0}}`), 0},
		{"five", []byte(`{"rev":{"perDay":5}}`), 5},
		{"negative", []byte(`{"rev":{"perDay":-1}}`), DefaultRevPerDay},
		{"over max", []byte(`{"rev":{"perDay":10000}}`), DefaultRevPerDay},
		{"wrong type", []byte(`{"rev":{"perDay":"20"}}`), DefaultRevPerDay},
		{"not json", []byte(`not json`), DefaultRevPerDay},
		{"new key present, rev absent", []byte(`{"new":{"perDay":5}}`), DefaultRevPerDay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RevPerDay(c.preset); got != c.want {
				t.Errorf("RevPerDay(%s) = %d, want %d", c.preset, got, c.want)
			}
		})
	}
}

func TestDueLookAheadMinutes(t *testing.T) {
	cases := []struct {
		name   string
		preset []byte
		want   int32
	}{
		{"nil", nil, DefaultDueLookAheadMinutes},
		{"empty object", []byte(`{}`), DefaultDueLookAheadMinutes},
		{"due present, lookAheadMinutes absent", []byte(`{"due":{}}`), DefaultDueLookAheadMinutes},
		{"zero", []byte(`{"due":{"lookAheadMinutes":0}}`), 0},
		{"thirty", []byte(`{"due":{"lookAheadMinutes":30}}`), 30},
		{"at max", []byte(`{"due":{"lookAheadMinutes":1440}}`), 1440},
		{"negative", []byte(`{"due":{"lookAheadMinutes":-1}}`), DefaultDueLookAheadMinutes},
		{"over max", []byte(`{"due":{"lookAheadMinutes":1441}}`), DefaultDueLookAheadMinutes},
		{"wrong type", []byte(`{"due":{"lookAheadMinutes":"30"}}`), DefaultDueLookAheadMinutes},
		{"not json", []byte(`not json`), DefaultDueLookAheadMinutes},
		{"new key present, due absent", []byte(`{"new":{"perDay":5}}`), DefaultDueLookAheadMinutes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DueLookAheadMinutes(c.preset); got != c.want {
				t.Errorf("DueLookAheadMinutes(%s) = %d, want %d", c.preset, got, c.want)
			}
		})
	}
}

func TestLeftToStudy(t *testing.T) {
	cases := []struct {
		name                              string
		newCount, learningCount, dueCount int64
		priority                          Priority
		newRemaining, totalRemaining      int32
		want                              int64
	}{
		// Mixed matches PriorityAllocate's no-cross-awareness branch when new+due doesn't exceed
		// totalRemaining -- exactly the pre-#118 independent-cap formula in that case.
		{"new under remaining", 5, 3, 5, PriorityMixed, 20, 200, 5 + 3 + 5},
		{"new over remaining", 25, 3, 5, PriorityMixed, 20, 200, 20 + 3 + 5},
		{"new equal to remaining", 20, 3, 5, PriorityMixed, 20, 200, 20 + 3 + 5},
		{"due under remaining", 5, 3, 5, PriorityMixed, 20, 200, 5 + 3 + 5},
		{"learning always passed through", 0, 42, 0, PriorityMixed, 0, 0, 42},
		{"zero new remaining", 5, 3, 5, PriorityMixed, 0, 200, 0 + 3 + 5},
		{"zero everything", 0, 0, 0, PriorityMixed, 0, 0, 0},
		// Mixed's independent new+due allowances can individually exceed totalRemaining once
		// summed (PriorityAllocate deliberately doesn't cross-cap them, see its doc comment) --
		// LeftToStudy clamps the sum itself so this never overstates what's actually servable.
		{"due over remaining, mixed clamps to total", 5, 3, 250, PriorityMixed, 20, 200, 200 + 3},
		{"due equal to remaining, mixed clamps to total", 5, 3, 200, PriorityMixed, 20, 200, 200 + 3},
		{"zero total remaining clamps mixed to zero", 5, 3, 5, PriorityMixed, 20, 0, 0 + 3},
		// #118: due/new priority split one shared total rather than each having its own,
		// so their sum can no longer exceed totalRemaining the way mixed's can.
		{"new priority backfills due", 1, 0, 20, PriorityNew, 5, 10, 1 + 0 + 9},
		{"due priority backfills new", 20, 0, 2, PriorityDue, 5, 10, 5 + 0 + 2},
		{"new priority, new ceiling below total", 20, 0, 20, PriorityNew, 5, 10, 5 + 0 + 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LeftToStudy(c.newCount, c.learningCount, c.dueCount, c.priority, c.newRemaining, c.totalRemaining); got != c.want {
				t.Errorf("LeftToStudy(%d, %d, %d, %q, %d, %d) = %d, want %d",
					c.newCount, c.learningCount, c.dueCount, c.priority, c.newRemaining, c.totalRemaining, got, c.want)
			}
		})
	}
}

func TestPriorityAllocate(t *testing.T) {
	cases := []struct {
		name                               string
		priority                           Priority
		newCeiling, totalRemaining         int32
		newAvailable, dueAvailable         int64
		wantNewAllowance, wantDueAllowance int32
	}{
		{"new priority, new scarce, due backfills", PriorityNew, 5, 10, 1, 20, 1, 9},
		{"new priority, new plentiful, ceiling binds", PriorityNew, 5, 10, 20, 20, 5, 5},
		{"new priority, total smaller than ceiling", PriorityNew, 20, 3, 20, 20, 3, 0},
		{"due priority, due scarce, new backfills", PriorityDue, 20, 10, 20, 2, 8, 2},
		{"due priority, due plentiful, total binds", PriorityDue, 20, 10, 20, 20, 0, 10},
		{"mixed, independent caps, no cross-awareness", PriorityMixed, 5, 10, 20, 20, 5, 10},
		{"mixed, both scarce", PriorityMixed, 5, 10, 2, 3, 2, 3},
		{"zero total", PriorityDue, 20, 0, 20, 20, 0, 0},
		{"zero ceiling under new priority", PriorityNew, 0, 10, 20, 20, 0, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotNew, gotDue := PriorityAllocate(c.priority, c.newCeiling, c.totalRemaining, c.newAvailable, c.dueAvailable)
			if gotNew != c.wantNewAllowance || gotDue != c.wantDueAllowance {
				t.Errorf("PriorityAllocate(%q, %d, %d, %d, %d) = (%d, %d), want (%d, %d)",
					c.priority, c.newCeiling, c.totalRemaining, c.newAvailable, c.dueAvailable,
					gotNew, gotDue, c.wantNewAllowance, c.wantDueAllowance)
			}
		})
	}
}

func TestRevRemaining(t *testing.T) {
	cases := []struct {
		name          string
		perDay        int32
		reviewedToday int64
		want          int32
	}{
		{"none reviewed", 200, 0, 200},
		{"partial", 200, 150, 50},
		{"exactly at limit", 200, 200, 0},
		{"over limit", 200, 250, 0},
		{"zero perDay", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RevRemaining(c.perDay, c.reviewedToday); got != c.want {
				t.Errorf("RevRemaining(%d, %d) = %d, want %d", c.perDay, c.reviewedToday, got, c.want)
			}
		})
	}
}
