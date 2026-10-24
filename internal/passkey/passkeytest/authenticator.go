// Package passkeytest is a software WebAuthn authenticator with one ES256
// credential, for tests. It plays both the browser and the authenticator: it
// takes the options a relying party sends ({"publicKey": {...}}) and returns
// the PublicKeyCredential JSON a browser would post back.
//
// It always signs for its own origin and RP ID (the host of the origin) and
// never checks the rpId in the options, so it can also stand in for a client
// at the wrong origin; catching that is the relying party's job.
package passkeytest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator data flags.
const (
	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagBE = 0x08 // backup eligible
	flagBS = 0x10 // backed up
	flagAT = 0x40 // attested credential data included
)

// COSE identifiers for an EC2 P-256 ES256 key.
const (
	coseKty, coseAlg, coseCrv, coseX, coseY = 1, 3, -1, -2, -3
	coseKtyEC2, coseAlgES256, coseCrvP256   = 2, -7, 1
)

var b64 = base64.RawURLEncoding

// Authenticator holds one credential. It is safe for concurrent use.
type Authenticator struct {
	mu         sync.Mutex
	origin     string
	rpID       string
	key        *ecdsa.PrivateKey
	credID     []byte
	userHandle []byte // set by Create
	signCount  uint32
	uv         bool
	synced     bool
}

// New returns an authenticator for origin (e.g. "http://localhost:8080").
// Its RP ID is the host of origin. User verification is on.
func New(origin string) *Authenticator {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		panic(fmt.Sprintf("passkeytest: bad origin %q", origin))
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	return &Authenticator{origin: u.Scheme + "://" + u.Host, rpID: strings.ToLower(u.Hostname()), key: key, credID: id, uv: true}
}

// SetUserVerified sets whether the UV flag is reported (a failed or skipped
// biometric check when false).
func (a *Authenticator) SetUserVerified(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.uv = v
}

// SetSynced makes the credential behave like a synced passkey (iCloud
// Keychain, Google Password Manager): authenticator data carries the
// backup-eligible and backed-up flags (BE, BS), and the sign count stays 0.
// Call it before Create: a credential's backup eligibility never changes, and
// relying parties refuse an assertion whose BE flag differs from enrolment.
func (a *Authenticator) SetSynced(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.synced = v
}

type descriptor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type creationOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
		PubKeyCredParams []struct {
			Type string `json:"type"`
			Alg  int    `json:"alg"`
		} `json:"pubKeyCredParams"`
		ExcludeCredentials []descriptor `json:"excludeCredentials"`
	} `json:"publicKey"`
}

type requestOptions struct {
	PublicKey struct {
		Challenge        string       `json:"challenge"`
		AllowCredentials []descriptor `json:"allowCredentials"`
	} `json:"publicKey"`
}

// decode reads a base64url value, with or without padding.
func decode(s string) ([]byte, error) { return b64.DecodeString(strings.TrimRight(s, "=")) }

func (a *Authenticator) listed(ds []descriptor) bool {
	for _, d := range ds {
		if id, err := decode(d.ID); err == nil && bytes.Equal(id, a.credID) {
			return true
		}
	}
	return false
}

func (a *Authenticator) flags() byte {
	f := byte(flagUP)
	if a.uv {
		f |= flagUV
	}
	if a.synced {
		f |= flagBE | flagBS
	}
	return f
}

func (a *Authenticator) clientData(typ string, challenge []byte) []byte {
	cd, _ := json.Marshal(struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}{typ, b64.EncodeToString(challenge), a.origin, false})
	return cd
}

// authData returns rpIdHash || flags || signCount, plus attested.
func (a *Authenticator) authData(flags byte, attested []byte) []byte {
	h := sha256.Sum256([]byte(a.rpID))
	d := append(h[:], flags)
	d = binary.BigEndian.AppendUint32(d, a.signCount)
	return append(d, attested...)
}

// Create answers navigator.credentials.create: attestation "none" with an EC2
// P-256 COSE key. It refuses when its credential is in excludeCredentials or
// ES256 is not offered.
func (a *Authenticator) Create(options []byte) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var o creationOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, fmt.Errorf("passkeytest: creation options: %w", err)
	}
	pk := o.PublicKey
	challenge, err := decode(pk.Challenge)
	if err != nil || len(challenge) == 0 {
		return nil, errors.New("passkeytest: creation options: bad challenge")
	}
	user, err := decode(pk.User.ID)
	if err != nil || len(user) == 0 {
		return nil, errors.New("passkeytest: creation options: bad user.id")
	}
	es256 := len(pk.PubKeyCredParams) == 0
	for _, p := range pk.PubKeyCredParams {
		es256 = es256 || (p.Type == "public-key" && p.Alg == coseAlgES256)
	}
	if !es256 {
		return nil, errors.New("passkeytest: ES256 not offered")
	}
	if a.listed(pk.ExcludeCredentials) {
		return nil, errors.New("passkeytest: credential excluded (already registered)")
	}

	point, err := a.key.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		return nil, err
	}
	enc, err := cbor.CTAP2EncOptions().EncMode()
	if err != nil {
		return nil, err
	}
	coseKey, err := enc.Marshal(map[int]any{
		coseKty: coseKtyEC2, coseAlg: coseAlgES256, coseCrv: coseCrvP256,
		coseX: point[1:33], coseY: point[33:65],
	})
	if err != nil {
		return nil, err
	}
	attested := make([]byte, 16) // zero AAGUID
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(a.credID)))
	attested = append(attested, a.credID...)
	attested = append(attested, coseKey...)
	attObj, err := enc.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": a.authData(a.flags()|flagAT, attested),
	})
	if err != nil {
		return nil, err
	}
	a.userHandle = user
	id := b64.EncodeToString(a.credID)
	return json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", challenge)),
			"attestationObject": b64.EncodeToString(attObj),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults":  map[string]any{},
		"authenticatorAttachment": "platform",
	})
}

// Get answers navigator.credentials.get: it signs authData ||
// SHA-256(clientDataJSON) with ASN.1 ECDSA and increments the sign count
// (which a synced credential leaves at 0). It refuses before Create and when
// allowCredentials is non-empty and omits its credential.
func (a *Authenticator) Get(options []byte) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.userHandle == nil {
		return nil, errors.New("passkeytest: no credential; call Create first")
	}
	var o requestOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, fmt.Errorf("passkeytest: request options: %w", err)
	}
	challenge, err := decode(o.PublicKey.Challenge)
	if err != nil || len(challenge) == 0 {
		return nil, errors.New("passkeytest: request options: bad challenge")
	}
	if len(o.PublicKey.AllowCredentials) > 0 && !a.listed(o.PublicKey.AllowCredentials) {
		return nil, errors.New("passkeytest: credential not in allowCredentials")
	}
	if !a.synced {
		a.signCount++
	}
	authData := a.authData(a.flags(), nil)
	clientData := a.clientData("webauthn.get", challenge)
	cdHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(bytes.Clone(authData), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		return nil, err
	}
	id := b64.EncodeToString(a.credID)
	return json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData),
			"authenticatorData": b64.EncodeToString(authData),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.userHandle),
		},
		"clientExtensionResults":  map[string]any{},
		"authenticatorAttachment": "platform",
	})
}
