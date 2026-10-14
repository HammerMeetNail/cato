package main

import (
	"bytes"
	"crypto/tls"
	"path/filepath"
	"strings"
	"testing"

	"cato/internal/config"
	"cato/internal/devtls"
)

func TestDevCertCommandPrintsSPKIAndIsIdempotent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "tls")
	var stdout, stderr bytes.Buffer
	if err := devCertCommand([]string{"--out", out}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	spki := strings.TrimSpace(stdout.String())
	want, err := devtls.SPKIFingerprint(filepath.Join(out, devtls.CertFile))
	if err != nil {
		t.Fatal(err)
	}
	if spki != want {
		t.Fatalf("stdout = %q, want only the SPKI %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), filepath.Join(out, devtls.CertFile)) {
		t.Errorf("stderr does not name the certificate: %s", stderr.String())
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(out, devtls.CertFile), filepath.Join(out, devtls.KeyFile)); err != nil {
		t.Fatal(err)
	}
	// Running again (e.g. each Playwright worker loading the config) reuses
	// the certificate, so every process agrees on the pin.
	stdout.Reset()
	if err := devCertCommand([]string{"--out", out}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != spki {
		t.Fatal("second run changed the certificate")
	}
}

func TestDevCertCommandRequiresOut(t *testing.T) {
	if err := devCertCommand(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing --out accepted")
	}
}

func TestPrepareTLSGeneratesDevCertificate(t *testing.T) {
	cfg := &config.Config{DevTLS: true, DevTLSDir: filepath.Join(t.TempDir(), "dev-tls")}
	if err := prepareTLS(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCertFile != filepath.Join(cfg.DevTLSDir, devtls.CertFile) || cfg.TLSKeyFile != filepath.Join(cfg.DevTLSDir, devtls.KeyFile) {
		t.Fatalf("paths = %q %q", cfg.TLSCertFile, cfg.TLSKeyFile)
	}
	if _, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareTLSRefusesPlainHTTP(t *testing.T) {
	if err := prepareTLS(&config.Config{}); err == nil || !strings.Contains(err.Error(), "HTTPS-only") {
		t.Fatalf("err = %v", err)
	}
	if err := prepareTLS(&config.Config{AllowInsecure: true}); err != nil {
		t.Fatalf("insecure test mode: %v", err)
	}
}
