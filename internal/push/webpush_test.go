package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 8291 Appendix A.
func TestEncryptMatchesRFC8291Vector(t *testing.T) {
	asPrivRaw, _ := b64.DecodeString("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	asPriv, err := ecdh.P256().NewPrivateKey(asPrivRaw)
	if err != nil {
		t.Fatal(err)
	}
	salt, _ := b64.DecodeString("DGv6ra1nlYgDCS1FRnbzlw")
	sub := Subscription{
		P256dh: "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		Auth:   "BTBZMqHH6r4Tts7J_aSIgg",
	}
	got, err := Encrypt(sub, []byte("When I grow up, I want to be a watermelon"), salt, asPriv)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("ciphertext\n got %s\nwant %s", b64.EncodeToString(got), want)
	}
}

// decrypt is the browser's side (RFC 8291 §3.4 in reverse).
func decrypt(t *testing.T, uaPriv *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub := body[21 : 21+idlen]
	if rs != 4096 {
		t.Fatalf("record size %d", rs)
	}
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := uaPriv.ECDH(as)
	info := append(append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...), asPub...)
	ikm := hkdf(auth, shared, info, 32)
	block, _ := aes.NewCipher(hkdf(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16))
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, hkdf(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12), body[21+idlen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestSendDeliversAnEncryptedSignedMessage(t *testing.T) {
	keys, _ := GenerateKeys()
	again, err := ParseKeys(keys.Marshal())
	if err != nil || again.PublicKey() != keys.PublicKey() {
		t.Fatalf("keys don't round-trip: %v", err)
	}
	uaPriv, _ := ecdh.P256().GenerateKey(nil)
	auth := []byte("0123456789abcdef")
	var gotBody []byte
	var gotAuth, gotEnc, gotTTL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth, gotEnc, gotTTL = r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"), r.Header.Get("TTL")
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	s := &Sender{Keys: keys, Subject: "mailto:admin@example.com", HTTP: srv.Client()}
	sub := Subscription{Endpoint: srv.URL + "/push/abc", P256dh: b64.EncodeToString(uaPriv.PublicKey().Bytes()), Auth: b64.EncodeToString(auth)}
	if err := s.Send(context.Background(), sub, []byte(`{"title":"Hi"}`), time.Hour); err != nil {
		t.Fatal(err)
	}
	if string(decrypt(t, uaPriv, auth, gotBody)) != `{"title":"Hi"}` || gotEnc != "aes128gcm" || gotTTL != "3600" {
		t.Fatalf("delivery: enc %q ttl %q", gotEnc, gotTTL)
	}
	// The VAPID JWT verifies with the advertised key and names the origin.
	parts := strings.SplitN(strings.TrimPrefix(gotAuth, "vapid t="), ", k=", 2)
	if len(parts) != 2 || parts[1] != keys.PublicKey() {
		t.Fatalf("authorization %q", gotAuth)
	}
	seg := strings.Split(parts[0], ".")
	sig, _ := b64.DecodeString(seg[2])
	sum := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	if !ecdsa.Verify(&keys.Private.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("JWT signature doesn't verify")
	}
	var claims map[string]any
	raw, _ := b64.DecodeString(seg[1])
	json.Unmarshal(raw, &claims)
	if claims["aud"] != srv.URL || claims["sub"] != "mailto:admin@example.com" {
		t.Fatalf("claims %v", claims)
	}
	sub.Endpoint = srv.URL + "/gone"
	if err := s.Send(context.Background(), sub, []byte(`{}`), time.Hour); err != ErrGone {
		t.Fatalf("410 should be ErrGone, got %v", err)
	}
	sub.Endpoint = "http://insecure.example/push"
	if err := s.Send(context.Background(), sub, []byte(`{}`), time.Hour); err == nil {
		t.Fatal("plain-HTTP endpoints must be refused")
	}
}
