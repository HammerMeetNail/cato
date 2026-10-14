package http

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// certReloader serves the configured key pair and picks up renewals (from
// an ACME client or the dev-cert generator) without a restart. Files are
// re-checked at most once per interval; a failed reload keeps the old pair.
type certReloader struct {
	certFile, keyFile string
	interval          time.Duration

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

func newCertReloader(certFile, keyFile string, interval time.Duration) (*certReloader, error) {
	c := &certReloader{certFile: certFile, keyFile: keyFile, interval: interval}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *certReloader) newestModTime() (time.Time, error) {
	var newest time.Time
	for _, f := range []string{c.certFile, c.keyFile} {
		info, err := os.Stat(f)
		if err != nil {
			return time.Time{}, err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest, nil
}

func (c *certReloader) load() error {
	mod, err := c.newestModTime()
	if err != nil {
		return fmt.Errorf("tls certificate: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("tls certificate: %w", err)
	}
	c.cert, c.modTime, c.checked = &pair, mod, time.Now()
	return nil
}

func (c *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.checked) >= c.interval {
		c.checked = time.Now()
		if mod, err := c.newestModTime(); err == nil && !mod.Equal(c.modTime) {
			if err := c.load(); err != nil {
				log.Printf("tls: keeping previous certificate: %v", err)
			} else {
				log.Printf("tls: reloaded certificate %s", c.certFile)
			}
		}
	}
	return c.cert, nil
}

var redirectHostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// redirectHandler answers every plain-HTTP request with a 308 to the HTTPS
// origin: CATO_BASE_URL when set, otherwise the request host on the TLS
// listener's port. It never serves application content or cookies.
func (s *Server) redirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var target string
		if s.cfg.BaseURL != "" {
			target = strings.TrimSuffix(s.cfg.BaseURL, "/") + r.URL.RequestURI()
		} else {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if net.ParseIP(host) == nil && !redirectHostRE.MatchString(host) {
				http.Error(w, "bad host", http.StatusBadRequest)
				return
			}
			if port := s.tlsPort(); port != "" && port != "443" {
				host = net.JoinHostPort(host, port)
			} else if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			target = "https://" + host + r.URL.RequestURI()
		}
		w.Header().Set("Connection", "close")
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}

func (s *Server) tlsPort() string {
	addr := s.cfg.ListenAddr
	if a := s.Addr(); a != nil {
		addr = a.String()
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}
