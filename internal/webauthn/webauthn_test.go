package webauthn

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"testing"
)

// cbor encodes the few shapes a test authenticator needs.
func cbor(v any) []byte {
	head := func(major byte, n int) []byte {
		switch {
		case n < 24:
			return []byte{major<<5 | byte(n)}
		case n < 256:
			return []byte{major<<5 | 24, byte(n)}
		default:
			return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
		}
	}
	switch x := v.(type) {
	case int:
		if x < 0 {
			return head(1, -1-x)
		}
		return head(0, x)
	case []byte:
		return append(head(2, len(x)), x...)
	case string:
		return append(head(3, len(x)), x...)
	case map[any]any:
		out := head(5, len(x))
		keys := make([]any, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return string(cbor(keys[i])) < string(cbor(keys[j])) })
		for _, k := range keys {
			out = append(out, cbor(k)...)
			out = append(out, cbor(x[k])...)
		}
		return out
	}
	panic("unsupported")
}

type authenticator struct {
	ec    *ecdsa.PrivateKey
	ed    ed25519.PrivateKey
	id    []byte
	count uint32
}

func (a *authenticator) coseKey() []byte {
	if a.ed != nil {
		return cbor(map[any]any{1: 1, 3: AlgEdDSA, -1: 6, -2: []byte(a.ed.Public().(ed25519.PublicKey))})
	}
	return cbor(map[any]any{1: 2, 3: AlgES256, -1: 1, -2: a.ec.X.FillBytes(make([]byte, 32)), -3: a.ec.Y.FillBytes(make([]byte, 32))})
}

func makeAuthData(rpID string, flags byte, count uint32, cred []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	b := append(h[:], flags)
	b = binary.BigEndian.AppendUint32(b, count)
	return append(b, cred...)
}

func clientJSON(typ string, challenge []byte, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": Encode(challenge), "origin": origin, "crossOrigin": false})
	return b
}

func (a *authenticator) create(rp RP, challenge []byte) (cd, att []byte) {
	cred := make([]byte, 16) // AAGUID
	cred = binary.BigEndian.AppendUint16(cred, uint16(len(a.id)))
	cred = append(append(cred, a.id...), a.coseKey()...)
	ad := makeAuthData(rp.ID, flagUP|flagUV|flagAT, a.count, cred)
	return clientJSON("webauthn.create", challenge, rp.Origin), cbor(map[any]any{"fmt": "none", "attStmt": map[any]any{}, "authData": ad})
}

func (a *authenticator) get(rpID, origin string, challenge []byte) (cd, ad, sig []byte) {
	a.count++
	cd = clientJSON("webauthn.get", challenge, origin)
	ad = makeAuthData(rpID, flagUP|flagUV, a.count, nil)
	sum := sha256.Sum256(cd)
	msg := append(append([]byte{}, ad...), sum[:]...)
	if a.ed != nil {
		return cd, ad, ed25519.Sign(a.ed, msg)
	}
	h := sha256.Sum256(msg)
	sig, _ = ecdsa.SignASN1(rand.Reader, a.ec, h[:])
	return cd, ad, sig
}

func TestRegisterAndLogin(t *testing.T) {
	rp := RP{ID: "cato.test", Origin: "https://cato.test:7180", Name: "Cato"}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	for name, a := range map[string]*authenticator{"es256": {ec: ec, id: []byte("credential-1")}, "eddsa": {ed: ed, id: []byte("credential-2")}} {
		t.Run(name, func(t *testing.T) {
			challenge := []byte("register-challenge-0123456789abcd")
			cd, att := a.create(rp, challenge)
			cred, err := rp.Register(challenge, cd, att)
			if err != nil {
				t.Fatal(err)
			}
			if string(cred.ID) != string(a.id) {
				t.Fatalf("credential id %q", cred.ID)
			}
			// Wrong challenge, origin or relying party fail.
			if _, err := rp.Register([]byte("other"), cd, att); !errors.Is(err, ErrChallenge) {
				t.Errorf("wrong challenge: %v", err)
			}
			if _, err := (RP{ID: rp.ID, Origin: "https://evil.test"}).Register(challenge, cd, att); !errors.Is(err, ErrOrigin) {
				t.Errorf("wrong origin: %v", err)
			}
			if _, err := (RP{ID: "evil.test", Origin: rp.Origin}).Register(challenge, cd, att); err == nil {
				t.Error("wrong rp id accepted")
			}

			login := []byte("login-challenge-0123456789abcdefg")
			cd2, ad, sig := a.get(rp.ID, rp.Origin, login)
			count, err := rp.Login(login, *cred, cd2, ad, sig)
			if err != nil {
				t.Fatal(err)
			}
			cred.SignCount = count
			// A tampered signature or replayed counter fail.
			bad := append([]byte{}, sig...)
			bad[len(bad)-1] ^= 1
			if _, err := rp.Login(login, *cred, cd2, ad, bad); err == nil {
				t.Error("bad signature accepted")
			}
			if _, err := rp.Login(login, *cred, cd2, ad, sig); err == nil {
				t.Error("replayed counter accepted")
			}
			cd3, ad3, sig3 := a.get(rp.ID, rp.Origin, login)
			if _, err := rp.Login([]byte("stale"), *cred, cd3, ad3, sig3); !errors.Is(err, ErrChallenge) {
				t.Errorf("stale challenge: %v", err)
			}
		})
	}
}

func FuzzDecodeCBOR(f *testing.F) {
	f.Add(cbor(map[any]any{"fmt": "none", "authData": []byte{1, 2, 3}, 1: -7}))
	f.Add([]byte{0x9f, 0xff})
	f.Add([]byte{0xbb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		decodeCBOR(b) // must not panic or allocate unboundedly
		parseAuthData(b, true)
		coseKey(b)
	})
}
