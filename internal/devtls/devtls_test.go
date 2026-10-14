package devtls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func loadLeaf(t *testing.T, certPath string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestEnsureGeneratesUsableCertificate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dev-tls")
	names := []string{"localhost", "127.0.0.1", "::1", "cato.test"}
	certPath, keyPath, err := Ensure(dir, names)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{certPath, keyPath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, info.Mode().Perm())
		}
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", info.Mode().Perm())
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("key pair does not load: %v", err)
	}
	cert := loadLeaf(t, certPath)
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("algorithm = %v, want ECDSA", cert.PublicKeyAlgorithm)
	}
	if life := cert.NotAfter.Sub(time.Now()); life < 29*24*time.Hour || life > 31*24*time.Hour {
		t.Errorf("validity = %v, want about 30 days", life)
	}
	for _, host := range []string{"localhost", "cato.test", "127.0.0.1", "::1"} {
		if err := cert.VerifyHostname(host); err != nil {
			t.Errorf("certificate does not cover %s: %v", host, err)
		}
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ExtKeyUsage = %v", cert.ExtKeyUsage)
	}
}

func TestEnsureReusesValidCertificate(t *testing.T) {
	dir := t.TempDir()
	names := []string{"localhost", "127.0.0.1"}
	certPath, _, err := Ensure(dir, names)
	if err != nil {
		t.Fatal(err)
	}
	first := loadLeaf(t, certPath)
	if _, _, err := Ensure(dir, []string{"127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if again := loadLeaf(t, certPath); !again.Equal(first) {
		t.Fatal("valid certificate covering the names was regenerated")
	}
	// A new name forces a fresh certificate.
	if _, _, err := Ensure(dir, []string{"localhost", "cato.test"}); err != nil {
		t.Fatal(err)
	}
	if again := loadLeaf(t, certPath); again.Equal(first) {
		t.Fatal("certificate missing a requested name was reused")
	}
}

func TestEnsureReplacesExpiringCertificate(t *testing.T) {
	dir := t.TempDir()
	if err := write(dir, []string{"localhost"}, time.Now().Add(-29*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, CertFile)
	old := loadLeaf(t, certPath)
	if _, _, err := Ensure(dir, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	if loadLeaf(t, certPath).Equal(old) {
		t.Fatal("certificate expiring within a week was reused")
	}
}

func TestEnsureReplacesMismatchedKey(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := Ensure(dir, []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	_, otherKey, err := Ensure(other, []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := os.ReadFile(otherKey)
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Ensure(dir, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("mismatched pair was not regenerated: %v", err)
	}
}

func TestSPKIFingerprint(t *testing.T) {
	dir := t.TempDir()
	certPath, _, err := Ensure(dir, []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := SPKIFingerprint(certPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(loadLeaf(t, certPath).RawSubjectPublicKeyInfo)
	if want := base64.StdEncoding.EncodeToString(sum[:]); got != want {
		t.Fatalf("SPKI = %s, want %s", got, want)
	}
}

func TestDefaultNames(t *testing.T) {
	names := DefaultNames()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate name %q", n)
		}
		seen[n] = true
		if ip := net.ParseIP(n); ip != nil && ip.IsLinkLocalUnicast() {
			t.Errorf("link-local address %q included", n)
		}
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", "cato.test"} {
		if !seen[want] {
			t.Errorf("missing %q in %v", want, names)
		}
	}
	// Every default name must be usable in a certificate.
	if _, _, err := Ensure(t.TempDir(), names); err != nil {
		t.Fatal(err)
	}
}
