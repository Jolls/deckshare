package render

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
)

// MediaResolver looks up the blob stored for an Anki media filename, as extracted from an
// <img src="filename.jpg"> the .apkg import left untouched in note field HTML (§7). ok is false
// when the import never saw that filename on this deck.
type MediaResolver func(filename string) (sha256 string, ok bool)

// RewriteMediaSrcs rewrites every relative <img src="..."> in already-rendered, sanitised card
// HTML to the /media/{sha256} route (docs/routes.md, Media section). Anki fields reference media
// by bare filename; resolve maps that filename -- NFC-normalised the same way collectMedia
// normalised it at import time (internal/apkg/media.go) -- to its content-addressed blob. An
// absolute URL is left alone (not an Anki media reference); an unresolved filename is left alone
// too, so the <img> 404s harmlessly instead of being silently hidden.
func RewriteMediaSrcs(cardHTML string, resolve MediaResolver) string {
	if resolve == nil || !strings.Contains(cardHTML, "<img") {
		return cardHTML
	}

	var out strings.Builder
	z := html.NewTokenizer(strings.NewReader(cardHTML))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data != "img" {
				out.Write(z.Raw())
				continue
			}
			for i, a := range tok.Attr {
				if a.Key != "src" || isAbsoluteURL(a.Val) {
					continue
				}
				if sha, ok := resolve(norm.NFC.String(a.Val)); ok {
					tok.Attr[i].Val = "/media/" + sha
				}
			}
			out.WriteString(tok.String())
		default:
			out.Write(z.Raw())
		}
	}
}

func isAbsoluteURL(raw string) bool {
	u, err := url.Parse(raw)
	return err != nil || u.IsAbs()
}
