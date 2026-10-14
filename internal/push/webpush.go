// Package push sends Web Push notifications (plan §8.2) without a
// third-party library: VAPID authentication (RFC 8292) and aes128gcm
// payload encryption (RFC 8291, RFC 8188). Keys are generated on first
// use and kept in instance_settings.
package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var b64 = base64.RawURLEncoding

// Keys is the server's VAPID key pair.
type Keys struct {
	Private *ecdsa.PrivateKey
}

// PublicKey is the applicationServerKey browsers subscribe with: the
// uncompressed P-256 point, base64url.
func (k Keys) PublicKey() string {
	pub, _ := k.Private.PublicKey.ECDH()
	return b64.EncodeToString(pub.Bytes())
}

// GenerateKeys makes a new VAPID key pair.
func GenerateKeys() (Keys, error) {
	pk, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return Keys{Private: pk}, err
}

// Marshal and ParseKeys store the private scalar as base64url.
func (k Keys) Marshal() string { return b64.EncodeToString(k.Private.D.FillBytes(make([]byte, 32))) }

func ParseKeys(s string) (Keys, error) {
	d, err := b64.DecodeString(s)
	if err != nil || len(d) != 32 {
		return Keys{}, errors.New("invalid VAPID key")
	}
	priv, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return Keys{}, err
	}
	pub := priv.PublicKey().Bytes()
	x, y := new(big.Int).SetBytes(pub[1:33]), new(big.Int).SetBytes(pub[33:])
	return Keys{Private: &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: new(big.Int).SetBytes(d)}}, nil
}

// Subscription is what PushManager.subscribe() returns.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Encrypt seals payload for a subscription (RFC 8291 §3.4) as one
// aes128gcm record. salt and the server's ephemeral key are random unless
// given (tests pass fixed ones).
func Encrypt(sub Subscription, payload []byte, salt []byte, asPriv *ecdh.PrivateKey) ([]byte, error) {
	uaPub, err := decodeKey(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	authSecret, err := decodeKey(sub.Auth)
	if err != nil || len(authSecret) != 16 {
		return nil, errors.New("auth secret must be 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	if asPriv == nil {
		if asPriv, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
	}
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
	}
	asPub := asPriv.PublicKey().Bytes()
	shared, err := asPriv.ECDH(ua)
	if err != nil {
		return nil, err
	}
	// IKM = HKDF(auth_secret, ecdh_secret, "WebPush: info" || 0 || ua_public || as_public, 32)
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPub...), asPub...)
	ikm := hkdf(authSecret, shared, keyInfo, 32)
	cek := hkdf(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdf(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record: the payload and the last-record delimiter (no padding).
	plain := append(append([]byte{}, payload...), 2)
	const recordSize = 4096
	if len(plain)+16 > recordSize {
		return nil, errors.New("payload too large for one record")
	}
	var head bytes.Buffer
	head.Write(salt)
	binary.Write(&head, binary.BigEndian, uint32(recordSize))
	head.WriteByte(byte(len(asPub)))
	head.Write(asPub)
	return gcm.Seal(head.Bytes(), nonce, plain, nil), nil
}

// hkdf is HKDF-SHA256 (extract then a single expand block).
func hkdf(salt, ikm, info []byte, n int) []byte {
	ext := hmac.New(sha256.New, salt)
	ext.Write(ikm)
	prk := ext.Sum(nil)
	exp := hmac.New(sha256.New, prk)
	exp.Write(info)
	exp.Write([]byte{1})
	return exp.Sum(nil)[:n]
}

func decodeKey(s string) ([]byte, error) {
	if b, err := b64.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// vapidJWT signs the claim push services check (RFC 8292 §2).
func vapidJWT(k Keys, endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signing := head + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.Private, sum[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return signing + "." + b64.EncodeToString(sig), nil
}

// ErrGone means the subscription no longer exists (404/410): delete it.
var ErrGone = errors.New("subscription expired")

// Sender posts encrypted messages to push services.
type Sender struct {
	Keys    Keys
	Subject string // mailto: or https: contact
	HTTP    *http.Client
	Now     func() time.Time
}

// Send delivers payload (JSON the service worker reads) to one browser.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, ttl time.Duration) error {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" {
		return errors.New("push endpoint must be https")
	}
	body, err := Encrypt(sub, payload, nil, nil)
	if err != nil {
		return err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	jwt, err := vapidJWT(s.Keys, sub.Endpoint, s.Subject, now)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.Keys.PublicKey())
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode >= 300:
		return fmt.Errorf("push service: HTTP %d", res.StatusCode)
	}
	return nil
}
