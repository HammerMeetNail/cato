package auth

import (
	"sync"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(bytes), err
}

func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// DummyHash is a bcrypt hash (at the real cost) of a random password. Login
// compares against it when the email is unknown, so a miss costs the same
// time as a wrong password and response timing doesn't reveal accounts.
func DummyHash() string {
	dummyHashOnce.Do(func() {
		h, err := HashPassword(RandomToken(16))
		if err != nil {
			panic(err)
		}
		dummyHash = h
	})
	return dummyHash
}
