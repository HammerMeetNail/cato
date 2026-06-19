package http

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"

	"cato/internal/config"
	"cato/internal/covers"
)

type Server struct {
	cfg *config.Config
	db  *sql.DB
	mux *http.ServeMux
}

func NewServer(cfg *config.Config, db *sql.DB) *Server {
	s := &Server{
		cfg: cfg,
		db:  db,
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)

	authHandler := NewAuthHandler(s.db, s.cfg)
	authHandler.Register(s.mux)

	gameHandler := NewGameHandler(s.db, s.cfg)
	gameHandler.Register(s.mux)

	libraryHandler := NewLibraryHandler(s.db)
	libraryHandler.Register(s.mux)

	s.mux.HandleFunc("/covers/", covers.ServeCover(s.db, s.cfg.CoverDir))

	// Page routes
	s.mux.HandleFunc("/login", s.servePage("login.html"))
	s.mux.HandleFunc("/library", s.servePage("index.html"))
	s.mux.HandleFunc("/settings", s.servePage("settings.html"))

	fs := http.FileServer(http.Dir(s.cfg.StaticDir))
	s.mux.Handle("/", fs)
}

func (s *Server) servePage(filename string) http.HandlerFunc {
	path := filepath.Join(s.cfg.StaticDir, filename)
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, path)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := "ok"
	dbStatus := "ok"

	if err := s.db.Ping(); err != nil {
		status = "degraded"
		dbStatus = "unreachable"
	}

	resp := map[string]string{
		"status":   status,
		"database": dbStatus,
	}

	w.Header().Set("Content-Type", "application/json")
	if status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) Start() error {
	server := &http.Server{
		Addr:    s.cfg.ListenAddr,
		Handler: s.mux,
	}
	return server.ListenAndServe()
}
