package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ListenAddr != ":7080" {
		t.Errorf("expected ListenAddr :7080, got %s", cfg.ListenAddr)
	}
	if cfg.DBPath != "data/cato.db" {
		t.Errorf("expected DBPath data/cato.db, got %s", cfg.DBPath)
	}
	if cfg.StaticDir != "web/static" {
		t.Errorf("expected StaticDir web/static, got %s", cfg.StaticDir)
	}
	if cfg.CoverDir != "data/covers" {
		t.Errorf("expected CoverDir data/covers, got %s", cfg.CoverDir)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	os.Setenv("CATO_LISTEN_ADDR", ":9090")
	os.Setenv("CATO_DB_PATH", "/tmp/test.db")
	defer func() {
		os.Unsetenv("CATO_LISTEN_ADDR")
		os.Unsetenv("CATO_DB_PATH")
	}()

	cfg := Load()
	if cfg.ListenAddr != ":9090" {
		t.Errorf("expected ListenAddr :9090, got %s", cfg.ListenAddr)
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("expected DBPath /tmp/test.db, got %s", cfg.DBPath)
	}
}

func TestGetEnv(t *testing.T) {
	if got := getEnv("NONEXISTENT_VAR", "default"); got != "default" {
		t.Errorf("expected 'default', got %q", got)
	}

	os.Setenv("CATO_TEST_VAR", "testvalue")
	defer os.Unsetenv("CATO_TEST_VAR")

	if got := getEnv("CATO_TEST_VAR", "default"); got != "testvalue" {
		t.Errorf("expected 'testvalue', got %q", got)
	}
}

func TestIntFromEnv(t *testing.T) {
	const key = "CATO_TEST_INT_VAR"
	defer os.Unsetenv(key)

	os.Unsetenv(key)
	if got := intFromEnv(key, 7); got != 7 {
		t.Errorf("unset: got %d, want 7", got)
	}
	for _, bad := range []string{"", "abc", "0", "-3"} {
		os.Setenv(key, bad)
		if got := intFromEnv(key, 7); got != 7 {
			t.Errorf("%q: got %d, want fallback 7", bad, got)
		}
	}
	os.Setenv(key, "42")
	if got := intFromEnv(key, 7); got != 42 {
		t.Errorf("42: got %d", got)
	}
}

func TestLoadAuthRateLimit(t *testing.T) {
	const key = "CATO_AUTH_RATE_LIMIT"
	old, had := os.LookupEnv(key)
	defer func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	}()

	os.Unsetenv(key)
	if got := Load().AuthRateLimit; got != 0 {
		t.Errorf("unset: got %d, want 0", got)
	}
	os.Setenv(key, "100")
	if got := Load().AuthRateLimit; got != 100 {
		t.Errorf("100: got %d", got)
	}
	os.Setenv(key, "bogus")
	if got := Load().AuthRateLimit; got != 0 {
		t.Errorf("bogus: got %d, want 0", got)
	}
}

func TestLoadTLSSettings(t *testing.T) {
	t.Setenv("CATO_TLS_CERT_FILE", "/certs/cert.pem")
	t.Setenv("CATO_TLS_KEY_FILE", "/certs/key.pem")
	t.Setenv("CATO_DEV_TLS", "1")
	t.Setenv("CATO_DEV_TLS_DIR", "/tmp/dev-tls")
	t.Setenv("CATO_HTTP_REDIRECT_ADDR", ":8080")
	t.Setenv("CATO_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.5 ::1")
	t.Setenv("CATO_ALLOW_INSECURE", "1")
	cfg := Load()
	if cfg.TLSCertFile != "/certs/cert.pem" || cfg.TLSKeyFile != "/certs/key.pem" {
		t.Fatalf("tls files: %q %q", cfg.TLSCertFile, cfg.TLSKeyFile)
	}
	if !cfg.DevTLS || cfg.DevTLSDir != "/tmp/dev-tls" || cfg.HTTPRedirectAddr != ":8080" || !cfg.AllowInsecure {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	want := []string{"10.0.0.0/8", "192.168.1.5/32", "::1/128"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("proxies: %v", cfg.TrustedProxies)
	}
	for i, p := range cfg.TrustedProxies {
		if p.String() != want[i] {
			t.Errorf("proxy %d = %s, want %s", i, p, want[i])
		}
	}
}

func TestLoadDevTLSDirDefault(t *testing.T) {
	t.Setenv("CATO_DEV_TLS_DIR", "")
	if got := Load().DevTLSDir; got != "data/dev-tls" {
		t.Errorf("DevTLSDir = %q", got)
	}
}

func TestHTTPSMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"plain", Config{}, false},
		{"http base", Config{BaseURL: "http://cato.lan"}, false},
		{"https base behind proxy", Config{BaseURL: "https://cato.example"}, true},
		{"native tls", Config{TLSCertFile: "c", TLSKeyFile: "k"}, true},
		{"dev tls", Config{DevTLS: true}, true},
		{"half tls", Config{TLSCertFile: "c"}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.HTTPS(); got != tc.want {
			t.Errorf("%s: HTTPS() = %v, want %v", tc.name, got, tc.want)
		}
		if got := tc.cfg.SecureCookies(); got != tc.want {
			t.Errorf("%s: SecureCookies() = %v, want %v", tc.name, got, tc.want)
		}
	}
	legacy := Config{CookieSecure: true}
	if !legacy.SecureCookies() || legacy.HTTPS() {
		t.Error("CATO_SECURE_COOKIES must force Secure without implying HTTPS mode")
	}
}

func TestValidateRequiresHTTPS(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"plain http refused", Config{}, "HTTPS-only"},
		{"plain http allowed for tests", Config{AllowInsecure: true}, ""},
		{"http base refused", Config{BaseURL: "http://10.0.0.42:7080", DevTLS: true}, "not https"},
		{"http base allowed for tests", Config{BaseURL: "http://10.0.0.42:7080", AllowInsecure: true}, ""},
		{"https base behind proxy", Config{BaseURL: "https://cato.example"}, ""},
		{"relative base", Config{BaseURL: "cato.example", DevTLS: true}, "absolute"},
		{"dev tls", Config{DevTLS: true}, ""},
		{"native tls", Config{TLSCertFile: "c", TLSKeyFile: "k"}, ""},
		{"cert without key", Config{TLSCertFile: "c", AllowInsecure: true}, "set together"},
		{"dev and native", Config{DevTLS: true, TLSCertFile: "c", TLSKeyFile: "k"}, "cannot be combined"},
		{"redirect without tls", Config{HTTPRedirectAddr: ":80", BaseURL: "https://cato.example"}, "needs Cato to serve TLS"},
		{"redirect with tls", Config{HTTPRedirectAddr: ":80", DevTLS: true}, ""},
	}
	for _, tc := range cases {
		err := tc.cfg.Validate()
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestValidateRejectsBadTrustedProxies(t *testing.T) {
	t.Setenv("CATO_TRUSTED_PROXIES", "10.0.0.0/8,not-an-ip")
	t.Setenv("CATO_DEV_TLS", "1")
	err := Load().Validate()
	if err == nil || !strings.Contains(err.Error(), "not-an-ip") {
		t.Fatalf("err = %v", err)
	}
}
