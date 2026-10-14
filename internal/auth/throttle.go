package auth

import (
	"sync"
	"time"
)

const (
	// freeLoginFailures are allowed before an account starts backing off.
	freeLoginFailures = 5
	maxLoginBackoff   = 15 * time.Minute
	// loginStateTTL forgets accounts with no failures for a day.
	loginStateTTL = 24 * time.Hour
)

// LoginThrottle backs off password guessing per account, independent of the
// client IP (which a botnet or a shared proxy makes meaningless). After
// freeLoginFailures consecutive failures each further attempt must wait
// 1s, 2s, 4s… up to maxLoginBackoff. Keys are normalized emails and are
// tracked whether or not the account exists, so the throttle can't be used
// to enumerate accounts. A successful login resets the key.
type LoginThrottle struct {
	mu        sync.Mutex
	state     map[string]loginState
	lastSweep time.Time
	// Now is the clock; tests replace it.
	Now func() time.Time
}

type loginState struct {
	failures int
	last     time.Time
}

func NewLoginThrottle() *LoginThrottle {
	return &LoginThrottle{state: make(map[string]loginState), Now: time.Now}
}

// Wait returns how long the key must wait before its next attempt (0 = now).
func (lt *LoginThrottle) Wait(key string) time.Duration {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	st, ok := lt.state[key]
	if !ok || st.failures < freeLoginFailures {
		return 0
	}
	wait := maxLoginBackoff
	if shift := st.failures - freeLoginFailures; shift < 20 {
		wait = min(time.Second<<shift, maxLoginBackoff)
	}
	if remaining := st.last.Add(wait).Sub(lt.Now()); remaining > 0 {
		return remaining
	}
	return 0
}

// Failure records a failed attempt.
func (lt *LoginThrottle) Failure(key string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	now := lt.Now()
	if now.Sub(lt.lastSweep) >= time.Hour {
		lt.lastSweep = now
		for k, st := range lt.state {
			if now.Sub(st.last) > loginStateTTL {
				delete(lt.state, k)
			}
		}
	}
	st := lt.state[key]
	st.failures++
	st.last = now
	lt.state[key] = st
}

// Success clears the key after a successful login.
func (lt *LoginThrottle) Success(key string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	delete(lt.state, key)
}

// Len reports how many keys are tracked.
func (lt *LoginThrottle) Len() int {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return len(lt.state)
}
