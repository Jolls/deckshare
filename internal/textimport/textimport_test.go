package textimport

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantNotes []Note
		wantErrs  []int // line numbers expected to error
	}{
		{
			name: "two valid lines",
			text: `{"fields":["Q1","A1"],"tags":["x"]}
{"fields":["Q2","A2"]}`,
			wantNotes: []Note{
				{Fields: []string{"Q1", "A1"}, Tags: []string{"x"}},
				{Fields: []string{"Q2", "A2"}},
			},
		},
		{
			name: "blank lines skipped",
			text: "\n{\"fields\":[\"Q1\",\"A1\"]}\n\n\n{\"fields\":[\"Q2\",\"A2\"]}\n\n",
			wantNotes: []Note{
				{Fields: []string{"Q1", "A1"}},
				{Fields: []string{"Q2", "A2"}},
			},
		},
		{
			name: "leading and trailing fence tolerated",
			text: "```json\n{\"fields\":[\"Q1\",\"A1\"]}\n```",
			wantNotes: []Note{
				{Fields: []string{"Q1", "A1"}},
			},
		},
		{
			name: "malformed value reported, valid values before it still kept",
			text: `{"fields":["Q1","A1"]}
not json at all
{"fields":["Q2","A2"]}`,
			wantNotes: []Note{
				{Fields: []string{"Q1", "A1"}},
			},
			wantErrs: []int{2},
		},
		{
			name:      "empty input",
			text:      "",
			wantNotes: nil,
		},
		{
			// Regression: a real AI reply ran every object together on one line with no
			// separator at all, despite the prompt asking for one per line. JSON objects are
			// self-delimiting, so this must still parse cleanly.
			name: "objects concatenated with no separator",
			text: `{"fields":["Q1","A1"]}{"fields":["Q2","A2"]}{"fields":["Q3","A3"]}`,
			wantNotes: []Note{
				{Fields: []string{"Q1", "A1"}},
				{Fields: []string{"Q2", "A2"}},
				{Fields: []string{"Q3", "A3"}},
			},
		},
		{
			// Regression for the exact reply that surfaced this bug (cloze cards for the
			// Lord's Prayer): five objects, no separators, cloze markup inside the field text.
			name: "real-world reply with no separators (issue report)",
			text: `{"fields":["{{c1::Our Father}}, {{c2::who art in heaven}}, {{c3::hallowed be thy name}}.",""]}` +
				`{"fields":["{{c1::Thy kingdom come}}, {{c2::thy will be done}}, {{c3::on earth as it is in heaven}}.",""]}` +
				`{"fields":["{{c1::Give us this day}} our {{c2::daily bread}}, and {{c3::forgive us our trespasses}}, as we {{c4::forgive those who trespass against us}}.",""]}` +
				`{"fields":["And {{c1::lead us not into temptation}}, but {{c2::deliver us from evil}}.",""]}` +
				`{"fields":["For {{c1::thine is the kingdom}}, and the {{c2::power}}, and the {{c3::glory}}, {{c4::forever and ever}}. Amen.",""]}`,
			wantNotes: []Note{
				{Fields: []string{"{{c1::Our Father}}, {{c2::who art in heaven}}, {{c3::hallowed be thy name}}.", ""}},
				{Fields: []string{"{{c1::Thy kingdom come}}, {{c2::thy will be done}}, {{c3::on earth as it is in heaven}}.", ""}},
				{Fields: []string{"{{c1::Give us this day}} our {{c2::daily bread}}, and {{c3::forgive us our trespasses}}, as we {{c4::forgive those who trespass against us}}.", ""}},
				{Fields: []string{"And {{c1::lead us not into temptation}}, but {{c2::deliver us from evil}}.", ""}},
				{Fields: []string{"For {{c1::thine is the kingdom}}, and the {{c2::power}}, and the {{c3::glory}}, {{c4::forever and ever}}. Amen.", ""}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notes, errs := Parse(tt.text)
			if !reflect.DeepEqual(notes, tt.wantNotes) {
				t.Errorf("Parse() notes = %#v, want %#v", notes, tt.wantNotes)
			}
			if len(errs) != len(tt.wantErrs) {
				t.Fatalf("Parse() errs = %v, want line numbers %v", errs, tt.wantErrs)
			}
			for i, wantLine := range tt.wantErrs {
				if errs[i].Line != wantLine {
					t.Errorf("errs[%d].Line = %d, want %d", i, errs[i].Line, wantLine)
				}
			}
		})
	}
}
