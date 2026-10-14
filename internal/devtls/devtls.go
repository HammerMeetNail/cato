// Package devtls creates self-signed development certificates with the
// standard library, so local runs and E2E tests get HTTPS (and therefore a
// secure context) without openssl, mkcert or any trust-store changes.
package devtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	CertFile = "cert.pem"
	KeyFile  = "key.pem"

	validity = 30 * 24 * time.Hour
	// renewBefore regenerates certificates close to expiry so a long-running
	// dev server never starts with one about to lapse.
	renewBefore = 7 * 24 * time.Hour
)

// Ensure returns the certificate and key paths in dir, generating a new
// ECDSA P-256 certificate when none exists, the pair is unusable, it expires
// within a week, or it does not cover every name.
func Ensure(dir string, names []string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, CertFile)
	keyPath = filepath.Join(dir, KeyFile)
	if reusable(certPath, keyPath, names) {
		return certPath, keyPath, nil
	}
	if err := write(dir, names, time.Now()); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func reusable(certPath, keyPath string, names []string) bool {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || pair.Leaf == nil {
		return false
	}
	if time.Until(pair.Leaf.NotAfter) < renewBefore {
		return false
	}
	for _, n := range names {
		if pair.Leaf.VerifyHostname(n) != nil {
			return false
		}
	}
	return true
}

func write(dir string, names []string, notBefore time.Time) error {
	if len(names) == 0 {
		return errors.New("devtls: no certificate names")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("devtls: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("devtls: chmod %s: %w", dir, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"Cato development"}, CommonName: names[0]},
		NotBefore:             notBefore.Add(-time.Hour),
		NotAfter:              notBefore.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("devtls: create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	// Key first: a crash between the two renames leaves a pair that fails to
	// load and is regenerated, never a certificate served with a stale key.
	if err := writeFile(filepath.Join(dir, KeyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, CertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".devtls-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SPKIFingerprint returns base64(SHA-256(SubjectPublicKeyInfo)) of the first
// certificate in certPath, the format Chromium's
// --ignore-certificate-errors-spki-list expects.
func SPKIFingerprint(certPath string) (string, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("devtls: %s has no certificate", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

var hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// DefaultNames covers loopback, the cato.test name the E2E harness maps to
// 127.0.0.1, this machine's hostname and its LAN addresses (so a phone on
// the same network can reach the dev server by IP).
func DefaultNames() []string {
	names := []string{"localhost", "127.0.0.1", "::1", "cato.test"}
	if host, err := os.Hostname(); err == nil {
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if hostnameRE.MatchString(host) {
			names = append(names, host)
			if !strings.Contains(host, ".") {
				names = append(names, host+".local")
			}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() || !ipNet.IP.IsGlobalUnicast() {
				continue
			}
			names = append(names, ipNet.IP.String())
		}
	}
	seen := make(map[string]bool, len(names))
	out := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
