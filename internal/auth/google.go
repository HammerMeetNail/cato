package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type GoogleUser struct {
	EmailVerified bool   `json:"email_verified"`
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func NewGoogleConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes: []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Endpoint: google.Endpoint,
	}
}

// googleUserInfoURL is the endpoint FetchGoogleUser calls for profile info.
// A var so tests can point it at a local server.
var googleUserInfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"

func FetchGoogleUser(ctx context.Context, config *oauth2.Config, code string) (*GoogleUser, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base := http.DefaultTransport
	if client, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && client.Transport != nil {
		base = client.Transport
	}
	client := &http.Client{Transport: googleBoundedTransport{base}, Timeout: 10 * time.Second}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	token, err := config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	client = config.Client(ctx, token)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch userinfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned status %d", resp.StatusCode)
	}

	var user GoogleUser
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&user); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing Google profile data")
	}
	if err := ValidateGoogleUser(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

func ValidateGoogleUser(user *GoogleUser) error {
	if user == nil || strings.TrimSpace(user.Sub) == "" || strings.TrimSpace(user.Email) == "" || !user.EmailVerified {
		return fmt.Errorf("Google profile requires a subject and verified email")
	}
	return nil
}

// Bound both token and profile bodies, including chunked responses. Reading
// under the shared request deadline also bounds a peer that stalls mid-body.
const googleResponseLimit = 1 << 20

type googleBoundedTransport struct{ base http.RoundTripper }

func (t googleBoundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, googleResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > googleResponseLimit {
		return nil, fmt.Errorf("Google response exceeds size limit")
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}
