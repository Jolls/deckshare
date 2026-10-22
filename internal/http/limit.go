package http

import (
	"net/http"
	"time"
)

// packageConcurrency bounds how many whole-package requests (POST /import, GET
// /decks/{id}/export) run at once. Each holds an entire .apkg in memory -- up to maxUploadBytes
// plus apkg.DefaultArchiveLimits' decompressed bytes on import, the generated package on export
// -- so the slot count, not the request count, bounds how many whole packages are in memory at
// once (#214). It does not bound one package's peak: a buffer growing by doubling, or the
// collection built before it, can briefly hold a multiple of the package size.
const packageConcurrency = 2

// packageRetryAfter is the Retry-After sent with a 503 when every slot is taken.
const packageRetryAfter = 30 * time.Second

// acquireSlot takes a slot without waiting. With every slot taken it answers 503 + Retry-After
// and returns false; otherwise the caller must defer the returned release, so every return
// path -- success, early return, error, panic -- frees the slot.
func acquireSlot(w http.ResponseWriter, slots chan struct{}) (release func(), ok bool) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		w.Header().Set("Retry-After", formatRetryAfter(packageRetryAfter))
		http.Error(w, "server busy, try again shortly", http.StatusServiceUnavailable)
		return nil, false
	}
}

// concurrencyLimit runs next only while it holds a slot in slots (see acquireSlot).
func concurrencyLimit(slots chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, ok := acquireSlot(w, slots)
		if !ok {
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}
