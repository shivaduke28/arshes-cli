package cmd

import (
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuthMiddleware(t *testing.T) {
	secret := "test-secret-token"
	logger := log.Default()
	handler := bearerAuthMiddleware(secret, logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "no authorization header",
			authHeader: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong token",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing Bearer prefix",
			authHeader: "test-secret-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "correct token",
			authHeader: "Bearer test-secret-token",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/mcp", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", rec.Code, tt.wantStatus)
			}

			if rec.Code == http.StatusUnauthorized {
				if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("got WWW-Authenticate %q, want %q", got, "Bearer")
				}
			}
		})
	}
}
