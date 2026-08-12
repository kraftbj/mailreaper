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
