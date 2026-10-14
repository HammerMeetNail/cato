// Package webauthn implements passkeys (plan §8.3): registering a
// credential and signing in with it. It accepts any attestation format
// without checking it (Cato doesn't restrict which authenticators people
// use), and verifies ES256, EdDSA and RS256 assertion signatures.
package webauthn

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// COSE algorithm identifiers.
const (
	AlgES256 = -7
	AlgEdDSA = -8
	AlgRS256 = -257
)

var b64 = base64.RawURLEncoding

// RP is the relying party: this server.
type RP struct {
	ID     string // the host name, never an IP address
	Origin string // https://host[:port] as the browser sees it
	Name   string
}

var (
	ErrChallenge = errors.New("the sign-in request expired; try again")
	ErrOrigin    = errors.New("the passkey was used on a different site")
	ErrVerify    = errors.New("the passkey response didn't verify")
)

type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

func (rp RP) checkClientData(raw []byte, typ string, challenge []byte) error {
	var cd clientData
	if err := json.Unmarshal(raw, &cd); err != nil {
		return ErrVerify
	}
	if cd.Type != typ {
		return ErrVerify
	}
	got, err := b64.DecodeString(cd.Challenge)
	if err != nil || len(challenge) == 0 || subtle.ConstantTimeCompare(got, challenge) != 1 {
		return ErrChallenge
	}
	if cd.Origin != rp.Origin {
		return ErrOrigin
	}
	return nil
}

// authData is the parsed authenticator data.
type authData struct {
	rpIDHash  []byte
	flags     byte
	signCount uint32
	credID    []byte
	coseKey   []byte
}

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
)

func parseAuthData(b []byte, wantCred bool) (*authData, error) {
	if len(b) < 37 {
		return nil, ErrVerify
	}
	a := &authData{rpIDHash: b[:32], flags: b[32], signCount: binary.BigEndian.Uint32(b[33:37])}
	if !wantCred {
		return a, nil
	}
	if a.flags&flagAT == 0 || len(b) < 37+18 {
		return nil, ErrVerify
	}
	rest := b[37+16:] // skip the AAGUID
	n := int(binary.BigEndian.Uint16(rest))
	rest = rest[2:]
	if n == 0 || n > 1023 || len(rest) < n {
		return nil, ErrVerify
	}
	a.credID, rest = rest[:n], rest[n:]
	_, after, err := decodeCBOR(rest)
	if err != nil {
		return nil, ErrVerify
	}
	a.coseKey = rest[:len(rest)-len(after)]
	return a, nil
}

func (rp RP) checkAuthData(a *authData) error {
	h := sha256.Sum256([]byte(rp.ID))
	if !bytes.Equal(a.rpIDHash, h[:]) || a.flags&flagUP == 0 {
		return ErrVerify
	}
	return nil
}

// Credential is a registered passkey.
type Credential struct {
	ID        []byte
	PublicKey []byte // COSE_Key
	Algorithm int
	SignCount uint32
}

// Register verifies navigator.credentials.create()'s response.
func (rp RP) Register(challenge, clientDataJSON, attestationObject []byte) (*Credential, error) {
	if err := rp.checkClientData(clientDataJSON, "webauthn.create", challenge); err != nil {
		return nil, err
	}
	obj, _, err := decodeCBOR(attestationObject)
	if err != nil {
		return nil, ErrVerify
	}
	m, ok := obj.(map[any]any)
	if !ok {
		return nil, ErrVerify
	}
	raw, ok := m["authData"].([]byte)
	if !ok {
		return nil, ErrVerify
	}
	a, err := parseAuthData(raw, true)
	if err != nil {
		return nil, err
	}
	if err := rp.checkAuthData(a); err != nil {
		return nil, err
	}
	alg, _, err := coseKey(a.coseKey)
	if err != nil {
		return nil, err
	}
	return &Credential{ID: a.credID, PublicKey: a.coseKey, Algorithm: alg, SignCount: a.signCount}, nil
}

// Login verifies navigator.credentials.get()'s response against the
// stored credential and returns the new signature counter.
func (rp RP) Login(challenge []byte, cred Credential, clientDataJSON, authenticatorData, signature []byte) (uint32, error) {
	if err := rp.checkClientData(clientDataJSON, "webauthn.get", challenge); err != nil {
		return 0, err
	}
	a, err := parseAuthData(authenticatorData, false)
	if err != nil {
		return 0, err
	}
	if err := rp.checkAuthData(a); err != nil {
		return 0, err
	}
	sum := sha256.Sum256(clientDataJSON)
	signed := append(append([]byte{}, authenticatorData...), sum[:]...)
	alg, pub, err := coseKey(cred.PublicKey)
	if err != nil {
		return 0, err
	}
	if !verify(alg, pub, signed, signature) {
		return 0, ErrVerify
	}
	// A counter that goes backwards means a cloned authenticator.
	if cred.SignCount > 0 && a.signCount != 0 && a.signCount <= cred.SignCount {
		return 0, ErrVerify
	}
	return a.signCount, nil
}

// coseKey decodes a COSE_Key (RFC 9053) to a Go public key.
func coseKey(raw []byte) (int, crypto.PublicKey, error) {
	v, _, err := decodeCBOR(raw)
	if err != nil {
		return 0, nil, ErrVerify
	}
	m, ok := v.(map[any]any)
	if !ok {
		return 0, nil, ErrVerify
	}
	kty, _ := m[int64(1)].(int64)
	alg, _ := m[int64(3)].(int64)
	switch {
	case kty == 2 && alg == AlgES256: // EC2, P-256
		crv, _ := m[int64(-1)].(int64)
		x, _ := m[int64(-2)].([]byte)
		y, _ := m[int64(-3)].([]byte)
		if crv != 1 || len(x) != 32 || len(y) != 32 {
			return 0, nil, ErrVerify
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return 0, nil, ErrVerify
		}
		return AlgES256, pub, nil
	case kty == 1 && alg == AlgEdDSA: // OKP, Ed25519
		crv, _ := m[int64(-1)].(int64)
		x, _ := m[int64(-2)].([]byte)
		if crv != 6 || len(x) != ed25519.PublicKeySize {
			return 0, nil, ErrVerify
		}
		return AlgEdDSA, ed25519.PublicKey(x), nil
	case kty == 3 && alg == AlgRS256:
		n, _ := m[int64(-1)].([]byte)
		e, _ := m[int64(-2)].([]byte)
		if len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return 0, nil, ErrVerify
		}
		return AlgRS256, &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}
	return 0, nil, ErrVerify
}

func verify(alg int, pub crypto.PublicKey, msg, sig []byte) bool {
	switch alg {
	case AlgES256:
		sum := sha256.Sum256(msg)
		return ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), sum[:], sig)
	case AlgEdDSA:
		return ed25519.Verify(pub.(ed25519.PublicKey), msg, sig)
	case AlgRS256:
		sum := sha256.Sum256(msg)
		return rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), crypto.SHA256, sum[:], sig) == nil
	}
	return false
}

// Decode is base64url as browsers send it (padding tolerated).
func Decode(s string) ([]byte, error) {
	if b, err := b64.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// Encode is base64url without padding.
func Encode(b []byte) string { return b64.EncodeToString(b) }

// Challenges remembers issued challenges for five minutes; each can be
// used once. In memory: a restart just means trying again.
type Challenges struct {
	mu sync.Mutex
	m  map[string]challenge
}

type challenge struct {
	userID  string
	expires time.Time
}

// New issues a challenge (bound to userID for registration, "" for login).
func (c *Challenges) New(userID string) []byte {
	b := make([]byte, 32)
	rand.Read(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]challenge{}
	}
	now := time.Now()
	for k, v := range c.m {
		if now.After(v.expires) {
			delete(c.m, k)
		}
	}
	if len(c.m) > 10000 {
		return nil // under attack: refuse rather than grow without bound
	}
	c.m[string(b)] = challenge{userID: userID, expires: now.Add(5 * time.Minute)}
	return b
}

// Take consumes the challenge named in clientDataJSON if it was issued (to
// userID) and hasn't expired, and returns it.
func (c *Challenges) Take(clientDataJSON []byte, userID string) ([]byte, error) {
	var cd clientData
	if err := json.Unmarshal(clientDataJSON, &cd); err != nil {
		return nil, ErrVerify
	}
	raw, err := b64.DecodeString(cd.Challenge)
	if err != nil {
		return nil, ErrChallenge
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[string(raw)]
	delete(c.m, string(raw))
	if !ok || time.Now().After(ch.expires) || ch.userID != userID {
		return nil, ErrChallenge
	}
	return raw, nil
}

// RPFor works out the relying party: from the base URL when set (behind a
// proxy), otherwise from the request's host. Passkeys need a host name;
// IP addresses are refused.
func RPFor(baseURL string, https bool, r *http.Request) (RP, error) {
	errIP := errors.New("Passkeys need Cato to be opened by its host name, not an IP address.")
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
			if net.ParseIP(u.Hostname()) != nil {
				return RP{}, errIP
			}
			return RP{ID: u.Hostname(), Origin: u.Scheme + "://" + u.Host, Name: "Cato"}, nil
		}
	}
	name := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		name = h
	}
	if name == "" || net.ParseIP(strings.Trim(name, "[]")) != nil {
		return RP{}, errIP
	}
	scheme := "http"
	if https || r.TLS != nil {
		scheme = "https"
	}
	return RP{ID: name, Origin: scheme + "://" + r.Host, Name: "Cato"}, nil
}
