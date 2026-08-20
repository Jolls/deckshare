package render

import "testing"

func TestRewriteMediaSrcs_ResolvesRelativeFilename(t *testing.T) {
	resolve := func(filename string) (string, bool) {
		if filename == "paris.jpg" {
			return "abc123", true
		}
		return "", false
	}
	got := RewriteMediaSrcs(`<img src="paris.jpg" alt="Paris">`, resolve)
	want := `<img src="/media/abc123" alt="Paris">`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteMediaSrcs_UnresolvedFilenameLeftAlone(t *testing.T) {
	resolve := func(string) (string, bool) { return "", false }
	got := RewriteMediaSrcs(`<img src="missing.jpg">`, resolve)
	want := `<img src="missing.jpg">`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteMediaSrcs_AbsoluteURLLeftAlone(t *testing.T) {
	resolve := func(string) (string, bool) {
		t.Fatal("resolve should not be called for an absolute URL")
		return "", false
	}
	got := RewriteMediaSrcs(`<img src="https://example.com/paris.jpg">`, resolve)
	want := `<img src="https://example.com/paris.jpg">`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteMediaSrcs_NoImgIsNoop(t *testing.T) {
	resolve := func(string) (string, bool) {
		t.Fatal("resolve should not be called when there is no <img>")
		return "", false
	}
	got := RewriteMediaSrcs(`<p>no images here</p>`, resolve)
	if got != `<p>no images here</p>` {
		t.Errorf("got %q, want unchanged input", got)
	}
}

func TestRewriteMediaSrcs_NilResolverIsNoop(t *testing.T) {
	got := RewriteMediaSrcs(`<img src="paris.jpg">`, nil)
	if got != `<img src="paris.jpg">` {
		t.Errorf("got %q, want unchanged input", got)
	}
}
