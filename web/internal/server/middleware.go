package server

import (
	"mime"
	"net"
	"net/http"
)

// defaultAllowedHosts is accepted when the server has no allowed_hosts
// override configured. Both the bracketed and bare forms of the IPv6
// loopback address are listed because net.SplitHostPort leaves the
// brackets in place when the Host header carries no port (see
// withAllowedHosts).
var defaultAllowedHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"[::1]":     true,
	"::1":       true,
}

// withAllowedHosts rejects requests whose Host header does not match an
// allowed hostname.
//
// withSameOrigin alone does not close DNS rebinding: an attacker page can
// be served from a hostname that resolves to 127.0.0.1, at which point the
// browser legitimately sends Sec-Fetch-Site: same-origin and can set
// Content-Type: application/json, so withSameOrigin lets the request
// through. The Host header still carries the attacker's chosen hostname in
// that scenario, not the loopback address, so checking it closes the gap.
//
// Applied to every method, not just mutations: an unauthenticated rebound
// GET would otherwise leak the whole mailbox index (rules, activity log,
// verdicts).
//
// allowed, when non-empty, replaces the default loopback set entirely --
// the escape hatch for anyone running this behind a reverse proxy with a
// real hostname (server.allowed_hosts in config.yaml).
func withAllowedHosts(allowed []string, next http.Handler) http.Handler {
	set := defaultAllowedHosts
	if len(allowed) > 0 {
		set = make(map[string]bool, len(allowed))
		for _, h := range allowed {
			set[h] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !set[host] {
			jsonError(w, "host not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

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
