package http

import "net/http"

// contentSecurityPolicy bounds what sanitised card HTML -- which under a shared deck is another
// user's HTML (architecture.md §8, §20) -- can do once it reaches the browser. Defence in depth
// behind internal/render's allowlist, never a replacement for it.
//
// Every source below traces to something concrete in this repo. Directive by directive:
//
//	default-src 'none'      Deny by default, then name each capability. This is what blocks
//	                        font-src/media-src/worker-src/frame-src/manifest-src without listing
//	                        them, and all four are correct to block: sanitisableElements has no
//	                        audio/video/iframe/object, and SanitiseCSS drops every at-rule, so
//	                        @font-face cannot exist. object-src is not repeated -- it falls back
//	                        here, so writing it would add drift, not constraint. frame-ancestors,
//	                        form-action and base-uri do NOT fall back, which is why they appear.
//	script-src 'self'       All JS is served from /static/ (web/templates/layout.html,
//	                        review.html). No template has an inline <script>, an inline event
//	                        handler, or a javascript: URL.
//	          'unsafe-eval' Forced by htmx 2.0.10: hx-vals="js:{...}" on #review-refill
//	                        (web/templates/review.html) is evaluated through the Function
//	                        constructor, which 'unsafe-eval' governs. Without it htmx raises
//	                        htmx:evalDisallowedError and refill breaks. This is the one source we
//	                        want gone and cannot remove yet -- docs/plans/57-csp-reviewer.md, Open
//	                        question 1. The grade-send POST no longer needs it: #99 moved it off
//	                        htmx (hx-vals/json-enc) to a direct fetch() in review.js. Alpine.js
//	                        (#166) also evaluates its x-data/x-show/@click expressions through
//	                        the Function constructor and would need this same source if it were
//	                        ever removed.
//	style-src 'self'        Covers web/static/app.css (#166) alongside the vendored JS.
//	          'unsafe-inline'  Forced, and a nonce would be strictly worse. Sanitised card HTML
//	                        carries inline style="" attributes on arbitrary elements
//	                        (sanitise.go's AllowAttrs("style")), and an attribute cannot take a
//	                        nonce -- while a nonce anywhere in style-src makes CSP ignore
//	                        'unsafe-inline' entirely. Nonceing review.html's note-type <style>
//	                        blocks would therefore kill every card's inline styles and tighten
//	                        nothing. What this permits is CSS injection, already bounded by the
//	                        property/value allowlist in internal/render/css.go; what CSP is here
//	                        for -- script injection -- is fully blocked by script-src above.
//	          https://cdn.jsdelivr.net  web/templates/layout.html loads Pico CSS from jsDelivr
//	                        (web/static/README.md records why Pico alone is not vendored).
//	                        Delete this source the day Pico moves under /static/.
//	img-src 'self'          The card-media origin policy. Card images resolve same-origin only:
//	                        Anki's relative-filename convention today (AllowRelativeURLs(true)),
//	                        /media/{sha256} once #60 lands. Remote origins are refused even
//	                        though sanitise.go still permits http/https on <img src> -- a shared
//	                        deck's <img src="https://attacker.example/px.jpg"> is a tracking
//	                        beacon handing a third party every reviewer's IP, UA and review
//	                        timing. Forced by multiuser; architecture.md §20 carries the row.
//	        data:           Pico's own form-control icons are data:image/svg+xml URIs. Safe: an
//	                        SVG loaded through <img>/background-image is script-disabled by spec,
//	                        and sanitise.go's scheme allowlist keeps data: off card <img src>.
//	connect-src 'self'      htmx's XHR to /api/reviews/next, review.js's fetch() and
//	                        navigator.sendBeacon to /api/reviews/batch (sendBeacon is governed by
//	                        connect-src -- omitting this silently breaks grade delivery).
//	form-action 'self'      Every form in web/templates/ posts same-origin. <form> is not in
//	                        sanitisableElements, so this guards against an injected form
//	                        exfiltrating a submitted {{type:Field}} answer.
//	frame-ancestors 'none'  internal/render/css.go names clickjacking against the rating buttons
//	                        as the reason position/z-index/transform are off the CSS allowlist;
//	                        that closes the inner half, this closes the outer. X-Frame-Options is
//	                        deliberately NOT also set -- frame-ancestors supersedes it.
//	base-uri 'none'         A <base> repoints every relative URL on the page at once, which
//	                        matters here precisely because card media is relative.
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

// securityHeaders sets the CSP on every response. It wraps OUTSIDE auth.Service.Middleware so the
// header is present on the CSRF 403 and on 404/500 error bodies too, not only on handler
// responses -- setting it before the inner handler runs is what guarantees it precedes any
// WriteHeader call below it.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		// Stop a browser from second-guessing our Content-Type. Load-bearing for /media/{sha256},
		// which serves imported .apkg blobs with a MIME derived from the filename extension.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Don't leak deck or note ids in the Referer on outbound navigation.
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
