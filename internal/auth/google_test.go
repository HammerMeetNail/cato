package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestGoogleResponseValidationAndLimits(t *testing.T) {
	for _, tc := range []struct{ name, token, profile string }{
		{"unverified", "", `{"sub":"subject","email":"g@example.com"}`},
		{"empty_subject", "", `{"sub":"","email":"g@example.com","email_verified":true}`},
		{"malformed", "", `{"sub":42,"email":"g@example.com","email_verified":true}`},
		{"invalid_json", "", `{`},
		{"large_profile", "", strings.Repeat(" ", googleResponseLimit+1)},
		{"large_token", strings.Repeat(" ", googleResponseLimit+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					if tc.token != "" {
						fmt.Fprint(w, tc.token)
					} else {
						fmt.Fprint(w, `{"access_token":"tok","token_type":"Bearer"}`)
					}
				} else {
					fmt.Fprint(w, tc.profile)
				}
			}))
			defer srv.Close()
			original := googleUserInfoURL
			googleUserInfoURL = srv.URL + "/userinfo"
			defer func() { googleUserInfoURL = original }()
			cfg := NewGoogleConfig("id", "secret", srv.URL+"/callback")
			cfg.Endpoint.TokenURL = srv.URL + "/token"
			cfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
			if user, err := FetchGoogleUser(context.Background(), cfg, "code"); err == nil {
				t.Fatalf("accepted response: %+v", user)
			}
		})
	}
}

func TestGoogleCancellationBoundsBothRequests(t *testing.T) {
	for _, stalledPath := range []string{"/token", "/userinfo"} {
		t.Run(stalledPath, func(t *testing.T) {
			started := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == stalledPath {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					close(started)
					<-r.Context().Done()
					return
				}
				fmt.Fprint(w, `{"access_token":"tok","token_type":"Bearer"}`)
			}))
			defer srv.Close()
			original := googleUserInfoURL
			googleUserInfoURL = srv.URL + "/userinfo"
			defer func() { googleUserInfoURL = original }()
			cfg := NewGoogleConfig("id", "secret", srv.URL+"/callback")
			cfg.Endpoint.TokenURL = srv.URL + "/token"
			cfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := FetchGoogleUser(ctx, cfg, "code"); done <- err }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("request never started")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancellation succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request did not cancel")
			}
		})
	}
}
