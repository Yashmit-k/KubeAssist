package api

import "net/http"

// NewCORSMiddleware wraps an http.Handler (our existing *http.ServeMux,
// unchanged) to add CORS headers, so a browser-based frontend running
// on a DIFFERENT origin (e.g. Vite's dev server at localhost:5173) can
// actually call this API. Without this, every browser fetch() to
// localhost:8080 from a page served at localhost:5173 is blocked by the
// browser itself before the request even leaves the tab — this is a
// browser-enforced security boundary called the Same-Origin Policy, and
// CORS headers are the server's way of explicitly opting specific
// origins OUT of that restriction.
//
// WHY A WRAPPING MIDDLEWARE INSTEAD OF EDITING EVERY HANDLER?
// CORS headers need to be present on EVERY response, including ones
// that never reach a real handler at all (a browser's OPTIONS
// "preflight" request, sent automatically before certain cross-origin
// requests, expects a response with no body — no handler in
// handlers.go needs to know this exists). A wrapping middleware
// intercepts every request before it reaches the router, handles
// preflight universally, and adds the response headers uniformly —
// this is why it lives in its own file and touches nothing else: it's
// a layer OVER the existing, already-tested router, not a change TO it.
//
// WHY ECHO THE SPECIFIC MATCHED ORIGIN BACK, RATHER THAN A BLANKET "*"?
// "*" (allow any origin) is the simplest option and works fine for a
// fully public, read-only API with no cookies/credentials involved.
// We echo the specific matched origin instead because it's the correct
// pattern the moment ANY future feature needs credentialed requests
// (cookies, or Access-Control-Allow-Credentials) — browsers reject "*"
// combined with credentials outright. Building it correctly now costs
// nothing and avoids a breaking change later.
func NewCORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// A request with no Origin header at all (curl, server-to
			// -server calls, Postman) isn't a browser cross-origin
			// request in the first place — CORS is exclusively a
			// browser mechanism, so there's nothing to add here, and we
			// just pass it straight through unchanged. This is WHY
			// every curl command we've run all session has worked fine
			// without any CORS headers present — curl was never subject
			// to the Same-Origin Policy to begin with.
			if origin != "" && (allowed[origin] || allowed["*"]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				// The session cookie must travel on cross-origin calls
				// when the UI is hosted on a different origin of the same
				// site. Only explicitly listed origins get credentials —
				// never the "*" wildcard.
				if allowed[origin] {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				// Vary: Origin tells any caching layer in front of this
				// server (a CDN, a reverse proxy) that the response
				// differs depending on the Origin header — without this,
				// a cache could serve one origin's CORS-approved
				// response to a DIFFERENT origin that should have been
				// rejected. Cheap to add, easy to forget, real bug class
				// once caching enters the picture.
				w.Header().Set("Vary", "Origin")
			}

			// Preflight requests: browsers automatically send an OPTIONS
			// request before certain cross-origin requests (any POST
			// with a JSON body qualifies) to ask "am I allowed to do
			// this?" before sending the real request. The browser
			// expects a fast, bodyless response confirming permission —
			// it never reaches our actual handlers, so we short-circuit
			// here rather than forwarding it into the router (which has
			// no OPTIONS routes registered at all, and would 404).
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
