package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Signup modes (CATO_SIGNUP). Until invites exist (plan phase P4), "invite"
// behaves like "closed". Every mode lets the first account on an empty
// instance be created, so a fresh install can always be bootstrapped.
const (
	SignupOpen   = "open"
	SignupInvite = "invite"
	SignupClosed = "closed"
)

type Config struct {
	ListenAddr       string
	DBPath           string
	StaticDir        string
	CoverDir         string
	GoogleKey        string
	GoogleSecret     string
	IGDBClientID     string
	IGDBClientSecret string
	CookieSecure     bool
	BaseURL          string
	// AuthRateLimit overrides the per-IP login/signup/password request limits
	// when > 0. Production leaves it unset (defaults: 10 login, 5 signup per
	// minute); test harnesses raise it so parallel API clients aren't
	// rate-limited as a single shared IP.
	AuthRateLimit int
	// SearchRateLimit is the per-IP limit on /api/games/search per minute
	// (CATO_SEARCH_RATE_LIMIT, default 120).
	SearchRateLimit int
	// Signup is one of SignupOpen, SignupInvite, SignupClosed (CATO_SIGNUP,
	// default closed). The zero value behaves as closed.
	Signup string

	// TLSCertFile/TLSKeyFile make Cato serve HTTPS itself. DevTLS generates
	// (and reuses) a self-signed certificate under DevTLSDir instead; main
	// fills TLSCertFile/TLSKeyFile with the generated paths.
	TLSCertFile string
	TLSKeyFile  string
	DevTLS      bool
	DevTLSDir   string
	// HTTPRedirectAddr, when set, answers plain HTTP with a 308 to HTTPS.
	HTTPRedirectAddr string
	// TrustedProxies are the peers whose X-Forwarded-For/-Proto headers are
	// believed. Empty means forwarded headers are always ignored.
	TrustedProxies []netip.Prefix
	// AllowInsecure permits running without HTTPS. Unit tests only.
	AllowInsecure bool

	// BackupDir receives the automatic pre-migration snapshots
	// (CATO_BACKUP_DIR, default "backups" next to the database).
	BackupDir string
	// LogFormat is "text" (default) or "json"; LogLevel debug|info|warn|error.
	LogFormat string
	LogLevel  string
	// MetricsToken, when set, is required as a Bearer token on /metrics.
	// Without it /metrics answers only private-network and loopback clients.
	MetricsToken string
	// Dev enables developer pages such as the component gallery (#dev/ui).
	Dev bool
	// WikidataLinks enables the weekly Wikidata crosswalk that adds
	// HowLongToBeat and PCGamingWiki links (it sends library games' IGDB
	// slugs to query.wikidata.org, so it is opt-in: CATO_WIKIDATA_LINKS=1).
	WikidataLinks bool

	// Integrations (plan §8), each optional: SteamKey enables Steam import
	// and playtime sync (STEAM_WEB_API_KEY); ITADKey price alerts through
	// IsThereAnyDeal (ITAD_API_KEY); UPCItemDBKey uses UPCitemdb's paid
	// tier for barcode lookups (without it the free trial tier is used).
	SteamKey     string
	ITADKey      string
	UPCItemDBKey string
	// SMTP sends the optional email digest (CATO_SMTP_HOST, _PORT, _USER,
	// _PASSWORD, _FROM). Without a host, email is off.
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	// PushContact is the VAPID subject push services can contact
	// (CATO_PUSH_CONTACT, a mailto: or https: URL; defaults to the base URL).
	PushContact string

	// trustedProxiesErr is reported by Validate so a typo in
	// CATO_TRUSTED_PROXIES fails startup instead of silently trusting nothing.
	trustedProxiesErr error
}

func Load() *Config {
	proxies, proxiesErr := ParseTrustedProxies(os.Getenv("CATO_TRUSTED_PROXIES"))
	return &Config{
		ListenAddr:        getEnv("CATO_LISTEN_ADDR", ":7080"),
		DBPath:            getEnv("CATO_DB_PATH", "data/cato.db"),
		StaticDir:         getEnv("CATO_STATIC_DIR", "web/static"),
		CoverDir:          getEnv("CATO_COVER_DIR", "data/covers"),
		GoogleKey:         os.Getenv("GOOGLE_KEY"),
		GoogleSecret:      os.Getenv("GOOGLE_SECRET"),
		IGDBClientID:      getEnv("IGDB_CLIENT_ID", os.Getenv("TWITCH_OAUTH_ID")),
		IGDBClientSecret:  getEnv("IGDB_CLIENT_SECRET", os.Getenv("TWITCH_OAUTH_SECRET")),
		CookieSecure:      os.Getenv("CATO_SECURE_COOKIES") == "true",
		BaseURL:           getEnv("CATO_BASE_URL", ""),
		AuthRateLimit:     intFromEnv("CATO_AUTH_RATE_LIMIT", 0),
		SearchRateLimit:   intFromEnv("CATO_SEARCH_RATE_LIMIT", 0),
		Signup:            strings.ToLower(getEnv("CATO_SIGNUP", SignupClosed)),
		TLSCertFile:       os.Getenv("CATO_TLS_CERT_FILE"),
		TLSKeyFile:        os.Getenv("CATO_TLS_KEY_FILE"),
		DevTLS:            boolFromEnv("CATO_DEV_TLS"),
		DevTLSDir:         getEnv("CATO_DEV_TLS_DIR", "data/dev-tls"),
		HTTPRedirectAddr:  os.Getenv("CATO_HTTP_REDIRECT_ADDR"),
		TrustedProxies:    proxies,
		AllowInsecure:     boolFromEnv("CATO_ALLOW_INSECURE"),
		trustedProxiesErr: proxiesErr,
		BackupDir:         os.Getenv("CATO_BACKUP_DIR"),
		LogFormat:         strings.ToLower(getEnv("CATO_LOG_FORMAT", "text")),
		LogLevel:          strings.ToLower(getEnv("CATO_LOG_LEVEL", "info")),
		MetricsToken:      os.Getenv("CATO_METRICS_TOKEN"),
		Dev:               boolFromEnv("CATO_DEV"),
		WikidataLinks:     boolFromEnv("CATO_WIKIDATA_LINKS"),
		SteamKey:          os.Getenv("STEAM_WEB_API_KEY"),
		ITADKey:           os.Getenv("ITAD_API_KEY"),
		UPCItemDBKey:      os.Getenv("UPCITEMDB_KEY"),
		SMTPHost:          os.Getenv("CATO_SMTP_HOST"),
		SMTPPort:          intFromEnv("CATO_SMTP_PORT", 587),
		SMTPUser:          os.Getenv("CATO_SMTP_USER"),
		SMTPPassword:      os.Getenv("CATO_SMTP_PASSWORD"),
		SMTPFrom:          os.Getenv("CATO_SMTP_FROM"),
		PushContact:       os.Getenv("CATO_PUSH_CONTACT"),
	}
}

// Summary describes the configuration for the startup log, without secrets
// (credentials are reported only as configured or not).
func (c *Config) Summary() []any {
	set := func(v string) string {
		if v == "" {
			return "unset"
		}
		return "set"
	}
	tls := "proxy"
	switch {
	case c.DevTLS:
		tls = "dev-self-signed"
	case c.ServesTLS():
		tls = "files"
	case !c.HTTPS():
		tls = "insecure"
	}
	return []any{
		"listen", c.ListenAddr, "base_url", c.BaseURL, "tls", tls,
		"http_redirect", c.HTTPRedirectAddr, "trusted_proxies", len(c.TrustedProxies),
		"db", c.DBPath, "covers", c.CoverDir, "static", c.StaticDir, "backups", c.BackupDir,
		"signup", c.SignupMode(), "igdb", set(c.IGDBClientID), "google", set(c.GoogleKey),
		"metrics_token", set(c.MetricsToken), "dev", c.Dev, "wikidata_links", c.WikidataLinks,
		"steam", set(c.SteamKey), "itad", set(c.ITADKey), "smtp", set(c.SMTPHost), "upcitemdb", set(c.UPCItemDBKey),
	}
}

// SignupIsOpen reports whether anyone may create an account (not just the
// first one on an empty instance).
func (c *Config) SignupIsOpen() bool { return c.Signup == SignupOpen }

// SignupMode is the effective mode name ("closed" for the zero value).
func (c *Config) SignupMode() string {
	if c.Signup == "" {
		return SignupClosed
	}
	return c.Signup
}

// ServesTLS reports whether Cato terminates TLS itself.
func (c *Config) ServesTLS() bool {
	return c.DevTLS || (c.TLSCertFile != "" && c.TLSKeyFile != "")
}

// HTTPS reports whether browsers reach Cato over HTTPS: either Cato serves
// TLS, or CATO_BASE_URL names an https:// origin (a TLS-terminating proxy).
// It selects __Host- cookies, HSTS and upgrade-insecure-requests.
func (c *Config) HTTPS() bool {
	if c.ServesTLS() {
		return true
	}
	u, err := url.Parse(c.BaseURL)
	return err == nil && u.Scheme == "https"
}

// SecureCookies reports whether cookies carry the Secure flag. HTTPS always
// implies it; CATO_SECURE_COOKIES remains a manual override.
func (c *Config) SecureCookies() bool {
	return c.HTTPS() || c.CookieSecure
}

// Validate rejects configurations that would serve Cato over plain HTTP or
// that are internally inconsistent. Plain HTTP needs CATO_ALLOW_INSECURE=1.
func (c *Config) Validate() error {
	if c.trustedProxiesErr != nil {
		return c.trustedProxiesErr
	}
	switch c.Signup {
	case "", SignupOpen, SignupInvite, SignupClosed:
	default:
		return fmt.Errorf("CATO_SIGNUP %q must be open, invite or closed", c.Signup)
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("CATO_TLS_CERT_FILE and CATO_TLS_KEY_FILE must be set together")
	}
	if c.DevTLS && c.TLSCertFile != "" {
		return errors.New("CATO_DEV_TLS cannot be combined with CATO_TLS_CERT_FILE/CATO_TLS_KEY_FILE")
	}
	if c.HTTPRedirectAddr != "" && !c.ServesTLS() {
		return errors.New("CATO_HTTP_REDIRECT_ADDR needs Cato to serve TLS (CATO_TLS_CERT_FILE/KEY_FILE or CATO_DEV_TLS=1)")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("CATO_BASE_URL %q must be an absolute https:// origin", c.BaseURL)
		}
		if u.Scheme != "https" && !c.AllowInsecure {
			return fmt.Errorf("CATO_BASE_URL %q is not https://; Cato is HTTPS-only (CATO_ALLOW_INSECURE=1 overrides, for tests only)", c.BaseURL)
		}
	}
	if !c.HTTPS() && !c.AllowInsecure {
		return errors.New("Cato is HTTPS-only: set CATO_TLS_CERT_FILE and CATO_TLS_KEY_FILE, CATO_DEV_TLS=1, " +
			"or CATO_BASE_URL=https://… behind a TLS-terminating proxy (CATO_ALLOW_INSECURE=1 overrides, for tests only)")
	}
	return nil
}

// ParseTrustedProxies parses a comma/space separated list of CIDR ranges or
// bare IP addresses.
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if p, err := netip.ParsePrefix(field); err == nil {
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("CATO_TRUSTED_PROXIES: %q is not an IP address or CIDR range", field)
		}
		out = append(out, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
	}
	return out, nil
}

func boolFromEnv(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func intFromEnv(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
