package config

import (
	"os"
	"strconv"
)

type Config struct {
	ListenAddr      string
	DBPath          string
	StaticDir       string
	CoverDir        string
	GoogleKey       string
	GoogleSecret    string
	IGDBClientID     string
	IGDBClientSecret string
	CookieSecure    bool
	BaseURL         string
	// AuthRateLimit overrides the per-IP login/signup/password request limits
	// when > 0. Production leaves it unset (defaults: 10 login, 5 signup per
	// minute); test harnesses raise it so parallel API clients aren't
	// rate-limited as a single shared IP.
	AuthRateLimit int
}

func Load() *Config {
	return &Config{
		ListenAddr:   getEnv("CATO_LISTEN_ADDR", ":7080"),
		DBPath:       getEnv("CATO_DB_PATH", "data/cato.db"),
		StaticDir:    getEnv("CATO_STATIC_DIR", "web/static"),
		CoverDir:     getEnv("CATO_COVER_DIR", "data/covers"),
		GoogleKey:       os.Getenv("GOOGLE_KEY"),
		GoogleSecret:    os.Getenv("GOOGLE_SECRET"),
		IGDBClientID:     getEnv("IGDB_CLIENT_ID", os.Getenv("TWITCH_OAUTH_ID")),
		IGDBClientSecret: getEnv("IGDB_CLIENT_SECRET", os.Getenv("TWITCH_OAUTH_SECRET")),
		CookieSecure:    os.Getenv("CATO_SECURE_COOKIES") == "true",
		BaseURL:         getEnv("CATO_BASE_URL", ""),
		AuthRateLimit:   intFromEnv("CATO_AUTH_RATE_LIMIT", 0),
	}
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
