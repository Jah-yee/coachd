package deviceapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

const (
	pairingTTL     = 600 * time.Second
	maxPending     = 5
	codeRetries    = 5  // further codes drawn after a clash with a pending one
	maxModelLen    = 32 // characters, after filtering
	pairingIDBytes = 16
	tokenBytes     = 32
	tokenLen       = 43 // tokenBytes as raw base64url
)

// pairingCreated answers POST /device/v1/pairings.
type pairingCreated struct {
	PairingID  string `json:"pairing_id"`
	Code       string `json:"code"`
	ExpiresInS int    `json:"expires_in_s"`
}

// pairingStatus answers GET /device/v1/pairings/{id}. Token is set only
// with status "paired", which is returned once.
type pairingStatus struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
}

// createPairing serves POST /device/v1/pairings. The body is optional; when
// present it is a JSON object whose "model" is kept after sanitizing.
func (s *Server) createPairing(w http.ResponseWriter, r *http.Request) {
	model, ok := readModel(r)
	if !ok {
		writeError(w, http.StatusBadRequest, msgMalformed)
		return
	}
	id := make([]byte, pairingIDBytes)
	rand.Read(id) // never fails (crypto/rand, Go 1.24+)
	now := s.d.Now()
	p := store.Pairing{ID: hex.EncodeToString(id), Model: model, CreatedAt: now, ExpiresAt: now.Add(pairingTTL)}
	for range 1 + codeRetries {
		code, err := newCode()
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		p.Code = code
		err = s.d.DB.CreatePairing(r.Context(), p, maxPending)
		switch {
		case errors.Is(err, store.ErrConflict):
			continue
		case errors.Is(err, store.ErrTooManyPairings):
			tooMany(w, msgTooManyPairs)
			return
		case err != nil:
			s.internalError(w, r, err)
			return
		}
		s.d.Log.InfoContext(r.Context(), "device pairing started", "component", "deviceapi", "outcome", "ok")
		writeJSON(w, http.StatusCreated, pairingCreated{PairingID: p.ID, Code: p.Code, ExpiresInS: int(pairingTTL / time.Second)})
		return
	}
	s.serverError(w, r, "no_free_code") // six clashes in a row: practically unreachable
}

// readModel reads the optional body and returns its sanitized model. A
// non-empty body must be declared application/json and be one JSON object
// (an array or a scalar fails to decode into the struct; null reads as no
// model); unknown fields are ignored.
func readModel(r *http.Request) (string, bool) {
	b, err := readBody(r)
	if err != nil {
		return "", false
	}
	if len(b) == 0 {
		return "", true
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return "", false
	}
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(b, &req) != nil {
		return "", false
	}
	return sanitizeModel(req.Model), true
}

// sanitizeModel removes every character outside [A-Za-z0-9 ._-], then keeps
// at most maxModelLen characters.
func sanitizeModel(m string) string {
	out := make([]byte, 0, maxModelLen)
	for i := 0; i < len(m) && len(out) < maxModelLen; i++ {
		c := m[i] // a multi-byte rune's bytes are all >= 0x80 and dropped
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == ' ' || c == '.' || c == '_' || c == '-' {
			out = append(out, c)
		}
	}
	return string(out)
}

// newCode draws a uniform 6-digit code from crypto/rand.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("deviceapi: pairing code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// pollPairing serves GET /device/v1/pairings/{id}: always 200. A token is
// generated for every well-formed poll and only its hash goes to the store;
// it is returned only when this poll claimed the confirmed pairing. An id of
// the wrong shape reads as expired, like an unknown one.
func (s *Server) pollPairing(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validHexID(id) {
		writeJSON(w, http.StatusOK, pairingStatus{Status: "expired"})
		return
	}
	tok, hash := newToken()
	status, devID, err := s.d.DB.ClaimPairing(r.Context(), id, hash, s.d.Now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	switch status {
	case "paired":
		s.d.Log.InfoContext(r.Context(), "device paired", "component", "deviceapi",
			"device_id", strconv.FormatInt(devID, 10), "outcome", "ok")
		writeJSON(w, http.StatusOK, pairingStatus{Status: "paired", Token: tok})
	case "pending":
		writeJSON(w, http.StatusOK, pairingStatus{Status: "pending"})
	default:
		writeJSON(w, http.StatusOK, pairingStatus{Status: "expired"})
	}
}

// validHexID reports whether id has the shape of a pairing or proposal id:
// 32 lower-case hex digits (128 random bits).
func validHexID(id string) bool {
	if len(id) != 2*pairingIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// newToken returns a device token (32 random bytes as raw base64url) and the
// SHA-256 of its text, which is all the store keeps.
func newToken() (string, []byte) {
	b := make([]byte, tokenBytes)
	rand.Read(b) // never fails (crypto/rand, Go 1.24+)
	tok := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(tok))
	return tok, sum[:]
}
