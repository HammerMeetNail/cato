package config

import "os"

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
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
