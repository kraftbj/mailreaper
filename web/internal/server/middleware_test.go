package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSameOriginGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	guarded := withSameOrigin(ok)

	tests := []struct {
		name        string
		method      string
		contentType string
		fetchSite   string
		wantStatus  int
	}{
		{"GET is never blocked", http.MethodGet, "", "cross-site", http.StatusOK},
		{"same-origin JSON POST allowed", http.MethodPost, "application/json", "same-origin", http.StatusOK},
		{"JSON POST with charset allowed", http.MethodPost, "application/json; charset=utf-8", "same-origin", http.StatusOK},
		{"no Sec-Fetch-Site (curl) allowed if JSON", http.MethodPost, "application/json", "", http.StatusOK},
		{"DELETE from UI allowed", http.MethodDelete, "application/json", "same-origin", http.StatusOK},
		{"cross-site JSON POST blocked", http.MethodPost, "application/json", "cross-site", http.StatusForbidden},
		{"form-encoded POST blocked", http.MethodPost, "application/x-www-form-urlencoded", "", http.StatusForbidden},
		{"text/plain POST blocked", http.MethodPost, "text/plain", "", http.StatusForbidden},
		{"missing content-type blocked", http.MethodPost, "", "", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/rules", strings.NewReader("{}"))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("got %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// TestAllowedHostsGuard covers Finding 3 from the whole-branch review:
// withSameOrigin alone does not defeat DNS rebinding -- a browser pointed
// at an attacker hostname that resolves to 127.0.0.1 sends
// Sec-Fetch-Site: same-origin and can set Content-Type: application/json
// legitimately, so the whole API stays reachable. withAllowedHosts checks
// the Host header itself, which rebinding cannot forge to look like the
// loopback address.
func TestAllowedHostsGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		allowed    []string
		host       string
		wantStatus int
	}{
		{"default allowlist accepts 127.0.0.1", nil, "127.0.0.1:8025", http.StatusOK},
		{"default allowlist accepts localhost", nil, "localhost:8025", http.StatusOK},
		{"default allowlist accepts bracketed IPv6 loopback", nil, "[::1]:8025", http.StatusOK},
		{"default allowlist rejects a rebound attacker hostname", nil, "evil.example.com", http.StatusForbidden},
		{"configured extra host is accepted", []string{"mail.example.net"}, "mail.example.net:8025", http.StatusOK},
		{"configuring an extra host drops the default loopback set", []string{"mail.example.net"}, "127.0.0.1:8025", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guarded := withAllowedHosts(tt.allowed, ok)
			req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("got %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
