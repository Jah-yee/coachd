// Package passkey runs the runner's WebAuthn ceremonies. Ceremony state lives
// in HMAC-signed tokens rather than server-side sessions; the WebAuthn
// challenge doubles as the single-use nonce the caller consumes in the store
// transaction that acts on a finished ceremony.
package passkey

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/stefiix92/coachd/internal/config"
)

// Purpose is what a ceremony token may be used for.
type Purpose string

const (
	PurposeEnroll  Purpose = "enroll"
	PurposeLogin   Purpose = "login"
	PurposeApprove Purpose = "approve"
	PurposeConsent Purpose = "consent" // approve an OAuth connection
)

// Ceremony is the signed state carried between Begin* and Finish*.
type Ceremony struct {
	Purpose      Purpose              `json:"p"`
	Session      webauthn.SessionData `json:"s"`
	ProposalID   string               `json:"pid,omitempty"`
	ContentHash  string               `json:"h,omitempty"`
	EnrollmentID int64                `json:"eid,omitempty"`
	// A consent binds the signed OAuth request token and the scopes the
	// runner chose, so the assertion approves exactly that request.
	Request string    `json:"rq,omitempty"`
	Scopes  []string  `json:"sc,omitempty"`
	Expires time.Time `json:"exp"`
}

// Nonce is the ceremony's single-use key: its WebAuthn challenge.
func (c Ceremony) Nonce() string { return c.Session.Challenge }

// ErrCeremony means a ceremony token is malformed, forged, for another
// purpose or expired. An expired token is ErrCeremonyExpired, which matches
// ErrCeremony too.
var ErrCeremony = errors.New("passkey: ceremony invalid or expired")

// ErrCeremonyExpired means a genuine ceremony token (valid MAC, purpose and
// binding) has expired: a page left open, not a forgery. errors.Is(err,
// ErrCeremony) holds for it.
var ErrCeremonyExpired error = expiredError{}

type expiredError struct{}

func (expiredError) Error() string { return "passkey: ceremony expired" }
func (expiredError) Unwrap() error { return ErrCeremony }

// keySize is the HMAC key length in bytes.
const keySize = 32

var b64 = base64.RawURLEncoding.Strict()

// LoadOrCreateKey returns the ceremony signing key stored at path as hex. On
// first use it creates the file with mode 0600 and 32 random bytes. It refuses
// a file readable by group or others. Errors never include the key.
func LoadOrCreateKey(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("passkey: create key file: %w", err)
	}
	key := make([]byte, keySize)
	rand.Read(key)
	_, err = f.WriteString(hex.EncodeToString(key) + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path) // never leave a partial key behind
		return nil, fmt.Errorf("passkey: write key file: %w", err)
	}
	return key, nil
}

func readKey(path string) ([]byte, error) {
	s, err := config.ReadSecretFile(path)
	if err != nil {
		return nil, fmt.Errorf("passkey: key file: %w", err)
	}
	key, err := hex.DecodeString(s)
	if err != nil || len(key) != keySize {
		// The decode error would quote a key character; say only what is wrong.
		return nil, fmt.Errorf("passkey: key file %s is malformed: want %d hex characters", path, 2*keySize)
	}
	return key, nil
}

// Signer signs and verifies ceremony tokens:
// base64url(json) + "." + base64url(HMAC-SHA256(key, json)).
type Signer struct{ key []byte }

// NewSigner returns a Signer for key (see LoadOrCreateKey). With a key shorter
// than 32 bytes Sign fails and Verify rejects every token.
func NewSigner(key []byte) *Signer { return &Signer{key: append([]byte(nil), key...)} }

func (s *Signer) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(payload)
	return m.Sum(nil)
}

// Sign returns the token for c.
func (s *Signer) Sign(c Ceremony) (string, error) {
	if len(s.key) < keySize {
		return "", fmt.Errorf("passkey: signing key shorter than %d bytes", keySize)
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("passkey: sign ceremony: %w", err)
	}
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(s.mac(payload)), nil
}

// Verify checks the MAC (hmac.Equal), the format, purpose == want, the
// binding and now < Expires. Nothing in the payload is read before the MAC
// checks out. A token that passes everything but has expired is
// ErrCeremonyExpired; every other failure is ErrCeremony, so a forged token
// never reads as a page left open.
func (s *Signer) Verify(token string, want Purpose, now time.Time) (Ceremony, error) {
	if len(s.key) < keySize {
		return Ceremony{}, ErrCeremony
	}
	p, m, ok := strings.Cut(token, ".")
	if !ok {
		return Ceremony{}, ErrCeremony
	}
	payload, err := b64.DecodeString(p)
	if err != nil {
		return Ceremony{}, ErrCeremony
	}
	sum, err := b64.DecodeString(m)
	if err != nil || !hmac.Equal(sum, s.mac(payload)) {
		return Ceremony{}, ErrCeremony
	}
	var c Ceremony
	if err := json.Unmarshal(payload, &c); err != nil {
		return Ceremony{}, ErrCeremony
	}
	if c.Purpose != want || c.Session.Challenge == "" || c.Expires.IsZero() {
		return Ceremony{}, ErrCeremony
	}
	switch c.Purpose {
	case PurposeApprove:
		if c.ProposalID == "" || c.ContentHash == "" {
			return Ceremony{}, ErrCeremony
		}
	case PurposeEnroll:
		if c.EnrollmentID <= 0 {
			return Ceremony{}, ErrCeremony
		}
	case PurposeConsent:
		if c.Request == "" || len(c.Scopes) == 0 {
			return Ceremony{}, ErrCeremony
		}
	}
	if !now.Before(c.Expires) {
		return Ceremony{}, ErrCeremonyExpired
	}
	return c, nil
}
