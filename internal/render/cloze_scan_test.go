package render

import "testing"

func TestScanCloze_HintSplitsOnFirstDoubleColon(t *testing.T) {
	spans := scanCloze("{{c1::text::hint::extra}}")
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].text != "text" {
		t.Errorf("text = %q, want %q", spans[0].text, "text")
	}
	if spans[0].hint != "hint::extra" {
		t.Errorf("hint = %q, want %q (further :: at depth 0 stays in the hint)", spans[0].hint, "hint::extra")
	}
}

func TestScanCloze_Nesting(t *testing.T) {
	spans := scanCloze("{{c1::a {{c2::b}}}}")
	if len(spans) != 1 {
		t.Fatalf("got %d top-level spans, want 1: %+v", len(spans), spans)
	}
	if spans[0].num != 1 {
		t.Errorf("num = %d, want 1", spans[0].num)
	}
	if spans[0].text != "a {{c2::b}}" {
		t.Errorf("text = %q, want %q", spans[0].text, "a {{c2::b}}")
	}
	inner := scanCloze(spans[0].text)
	if len(inner) != 1 || inner[0].num != 2 || inner[0].text != "b" {
		t.Errorf("inner = %+v", inner)
	}
}

func TestScanCloze_UnterminatedIsNotASpan(t *testing.T) {
	spans := scanCloze("{{c1::no close")
	if len(spans) != 0 {
		t.Errorf("got %d spans, want 0: %+v", len(spans), spans)
	}
}

func TestScanCloze_MultipleTopLevel(t *testing.T) {
	spans := scanCloze("{{c1::a}} middle {{c2::b}}")
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
	if spans[0].num != 1 || spans[1].num != 2 {
		t.Errorf("nums = %d,%d want 1,2", spans[0].num, spans[1].num)
	}
}
