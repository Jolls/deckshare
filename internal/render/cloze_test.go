package render

import (
	"reflect"
	"testing"
)

func TestClozeOrdinals(t *testing.T) {
	tests := []struct {
		name   string
		fields []string
		want   []int32
	}{
		{"single marker", []string{"{{c1::hidden}}"}, []int32{1}},
		{"hint form", []string{"{{c1::hidden::hint}}"}, []int32{1}},
		{"duplicate numbers across fields", []string{"{{c1::a}} {{c2::b}}", "{{c1::c}}"}, []int32{1, 2}},
		{"out of order numbers", []string{"{{c3::a}} {{c1::b}} {{c2::c}}"}, []int32{1, 2, 3}},
		{"c0 ignored", []string{"{{c0::a}} {{c1::b}}"}, []int32{1}},
		{"leading zero still parses", []string{"{{c01::a}}"}, []int32{1}},
		{"unterminated marker", []string{"{{c1:not a marker}}"}, nil},
		{"plain text containing c1::", []string{"see c1::example for details"}, nil},
		{"empty input", []string{""}, nil},
		{"no fields", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClozeOrdinals(tt.fields)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ClozeOrdinals(%v) = %v, want %v", tt.fields, got, tt.want)
			}
		})
	}
}
