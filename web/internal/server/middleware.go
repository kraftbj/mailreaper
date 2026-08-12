package server

import (
	"mime"
	"net/http"
)

// withSameOrigin rejects state-changing requests that did not originate from
// the dashboard itself.
//
// The API has no authentication, so without this guard any page the user
// visits while the server is running can create rules or trigger a rescan.
// Two checks together defeat form-based CSRF: a cross-origin <form> cannot
// send Content-Type: application/json (forms may only send text/plain,
// multipart/form-data, or application/x-www-form-urlencoded), and it cannot
// forge Sec-Fetch-Site. The bundled UI already sends application/json on
// every mutation (web/ui/js/app.js), so no legitimate client is affected.
//
// This is defense against the browser-driven attack only. It is not
// authentication: any local process can still call the API directly. Real
// auth belongs in a reverse proxy in front of this server.
func withSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		// Browsers set Sec-Fetch-Site on every request. Absent means a
		// non-browser client (curl, a script), which CSRF does not apply to.
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
			// allowed
		default:
			jsonError(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}

		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			jsonError(w, "expected Content-Type: application/json", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
