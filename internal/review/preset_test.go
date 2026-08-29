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
		newRemaining, revRemaining        int32
		want                              int64
	}{
		{"new under remaining", 5, 3, 5, 20, 200, 5 + 3 + 5},
		{"new over remaining", 25, 3, 5, 20, 200, 20 + 3 + 5},
		{"new equal to remaining", 20, 3, 5, 20, 200, 20 + 3 + 5},
		{"due under remaining", 5, 3, 5, 20, 200, 5 + 3 + 5},
		{"due over remaining", 5, 3, 250, 20, 200, 5 + 3 + 200},
		{"due equal to remaining", 5, 3, 200, 20, 200, 5 + 3 + 200},
		{"learning always passed through", 0, 42, 0, 0, 0, 42},
		{"zero new remaining", 5, 3, 5, 0, 200, 0 + 3 + 5},
		{"zero rev remaining", 5, 3, 5, 20, 0, 5 + 3 + 0},
		{"zero everything", 0, 0, 0, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LeftToStudy(c.newCount, c.learningCount, c.dueCount, c.newRemaining, c.revRemaining); got != c.want {
				t.Errorf("LeftToStudy(%d, %d, %d, %d, %d) = %d, want %d",
					c.newCount, c.learningCount, c.dueCount, c.newRemaining, c.revRemaining, got, c.want)
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
