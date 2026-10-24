package passkey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/stefiix92/coachd/internal/store"
)

// ErrNoPasskeys means no active passkey is enrolled, so no assertion can begin.
var ErrNoPasskeys = errors.New("passkey: no passkey enrolled")

// ErrInternal marks an infrastructure failure inside a ceremony (a store
// query, a stored credential that does not decode): an outage, not a failed
// check, so callers answer 500 and record no passkey failure. Every other
// Finish* error is a failed check.
var ErrInternal = errors.New("passkey: internal error")

// internal wraps an infrastructure failure in ErrInternal.
func internal(op string, err error) error {
	return fmt.Errorf("passkey: %s: %w: %w", op, ErrInternal, err)
}

// ErrUnknownCredential means an assertion came from a credential that is not
// enrolled or has been revoked.
var ErrUnknownCredential = errors.New("passkey: unknown or revoked credential")

// The single runner, a fixed WebAuthn user.
const (
	rpName          = "coachd"
	userHandle      = "coachd-runner"
	userName        = "runner"
	userDisplayName = "Runner"
)

type runner struct{ creds []webauthn.Credential }

func (runner) WebAuthnID() []byte                           { return []byte(userHandle) }
func (runner) WebAuthnName() string                         { return userName }
func (runner) WebAuthnDisplayName() string                  { return userDisplayName }
func (r runner) WebAuthnCredentials() []webauthn.Credential { return r.creds }

// Service runs enrolment and assertion ceremonies for the runner. It keeps no
// ceremony state: Begin* returns a signed token that Finish* verifies.
type Service struct {
	wa     *webauthn.WebAuthn
	signer *Signer
	db     *store.DB
	now    func() time.Time
}

// New configures the relying party from publicURL: RP ID is its host, the
// only allowed origin is its scheme and host. now drives ceremony expiry.
func New(publicURL string, s *Signer, db *store.DB, now func() time.Time) (*Service, error) {
	u, err := url.Parse(publicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return nil, fmt.Errorf("passkey: public URL %q must be an absolute http(s) URL", publicURL)
	}
	if s == nil || db == nil {
		return nil, errors.New("passkey: signer and store are required")
	}
	if now == nil {
		now = time.Now
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          strings.ToLower(u.Hostname()),
		RPDisplayName: rpName,
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: protocol.ResidentKeyNotRequired(),
			ResidentKey:        protocol.ResidentKeyRequirementPreferred,
			UserVerification:   protocol.VerificationRequired,
		},
		AttestationPreference: protocol.PreferNoAttestation,
	})
	if err != nil {
		return nil, fmt.Errorf("passkey: %w", err)
	}
	return &Service{wa: wa, signer: s, db: db, now: now}, nil
}

// activeCredentials decodes every active passkey's stored credential.
func (s *Service) activeCredentials(ctx context.Context) ([]webauthn.Credential, error) {
	pks, err := s.db.ActivePasskeys(ctx)
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(pks))
	for _, pk := range pks {
		c, err := decodeCredential(pk)
		if err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func decodeCredential(pk store.Passkey) (webauthn.Credential, error) {
	var c webauthn.Credential
	if err := json.Unmarshal(pk.CredentialJSON, &c); err != nil {
		return c, fmt.Errorf("passkey: stored credential %d: %w", pk.ID, err)
	}
	if !bytes.Equal(c.ID, pk.CredentialID) {
		return c, fmt.Errorf("passkey: stored credential %d: ID mismatch", pk.ID)
	}
	return c, nil
}

func (s *Service) sign(c Ceremony, options any) ([]byte, string, error) {
	opts, err := json.Marshal(options)
	if err != nil {
		return nil, "", fmt.Errorf("passkey: options: %w", err)
	}
	token, err := s.signer.Sign(c)
	if err != nil {
		return nil, "", err
	}
	return opts, token, nil
}

// Options are the browser's PublicKeyCredential{Creation,Request}Options as
// go-webauthn marshals them: {"publicKey": {...}}. The token is the signed
// Ceremony; the browser posts it back to the matching Finish* unchanged.

// BeginEnroll starts registering a new passkey for the enrolment. Existing
// active passkeys are excluded so an authenticator cannot register twice.
// The enrolment itself is not checked here; store.EnrollPasskey does that.
func (s *Service) BeginEnroll(ctx context.Context, enrollmentID int64, expires time.Time) (options []byte, token string, err error) {
	if enrollmentID <= 0 {
		return nil, "", errors.New("passkey: enrolment ID required")
	}
	creds, err := s.activeCredentials(ctx)
	if err != nil {
		return nil, "", err
	}
	creation, session, err := s.wa.BeginRegistration(runner{creds},
		webauthn.WithExclusions(webauthn.Credentials(creds).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation))
	if err != nil {
		return nil, "", fmt.Errorf("passkey: begin enrol: %w", err)
	}
	return s.sign(Ceremony{Purpose: PurposeEnroll, Session: *session, EnrollmentID: enrollmentID, Expires: expires}, creation)
}

// NewPasskey is a verified new credential, ready for store.EnrollPasskey.
type NewPasskey struct {
	CredentialID, CredentialJSON []byte
	Ceremony                     Ceremony
}

// FinishEnroll verifies the authenticator's attestation ("none") with user
// verification. It does not store anything or consume the nonce. An
// infrastructure failure is ErrInternal; any other error is a failed check.
func (s *Service) FinishEnroll(ctx context.Context, token string, credential []byte) (NewPasskey, error) {
	c, err := s.signer.Verify(token, PurposeEnroll, s.now())
	if err != nil {
		return NewPasskey{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return NewPasskey{}, fmt.Errorf("passkey: enrol: %w", err)
	}
	session := c.Session
	session.UserVerification = protocol.VerificationRequired
	cred, err := s.wa.CreateCredential(runner{}, session, parsed)
	if err != nil {
		return NewPasskey{}, fmt.Errorf("passkey: enrol: %w", err)
	}
	cj, err := json.Marshal(cred)
	if err != nil {
		return NewPasskey{}, internal("enrol", err)
	}
	return NewPasskey{CredentialID: cred.ID, CredentialJSON: cj, Ceremony: c}, nil
}

// BeginAssert starts a login or approval assertion listing every active
// passkey. An approval binds the proposal ID and content hash. It returns
// ErrNoPasskeys when none is active. A consent begins with BeginConsent.
func (s *Service) BeginAssert(ctx context.Context, p Purpose, proposalID, contentHash string, expires time.Time) (options []byte, token string, err error) {
	switch p {
	case PurposeLogin:
	case PurposeApprove:
		if proposalID == "" || contentHash == "" {
			return nil, "", errors.New("passkey: approval needs a proposal ID and content hash")
		}
	default:
		return nil, "", fmt.Errorf("passkey: cannot assert for purpose %q", p)
	}
	return s.beginAssert(ctx, Ceremony{Purpose: p, ProposalID: proposalID, ContentHash: contentHash, Expires: expires})
}

// BeginConsent starts the assertion that approves an OAuth connection. The
// ceremony binds request, the signed authorization request token, and the
// scopes the runner chose, so the finish takes neither from the browser. It
// returns ErrNoPasskeys when none is active.
func (s *Service) BeginConsent(ctx context.Context, request string, scopes []string, expires time.Time) (options []byte, token string, err error) {
	if request == "" || len(scopes) == 0 {
		return nil, "", errors.New("passkey: consent needs a request and scopes")
	}
	return s.beginAssert(ctx, Ceremony{Purpose: PurposeConsent, Request: request, Scopes: scopes, Expires: expires})
}

// beginAssert mints assertion options over every active passkey and signs c
// with the new session.
func (s *Service) beginAssert(ctx context.Context, c Ceremony) ([]byte, string, error) {
	creds, err := s.activeCredentials(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(creds) == 0 {
		return nil, "", ErrNoPasskeys
	}
	assertion, session, err := s.wa.BeginLogin(runner{creds}, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", fmt.Errorf("passkey: begin assert: %w", err)
	}
	c.Session = *session
	return s.sign(c, assertion)
}

// Assertion is a verified assertion. CredentialJSON is the credential with its
// updated sign count and flags, for store.UsePasskey.
type Assertion struct {
	PasskeyID      int64
	CredentialJSON []byte
	Ceremony       Ceremony
}

// FinishAssert verifies the assertion against an active passkey. It never
// consumes the nonce and does not know what the ceremony authorises, so the
// caller must, in the one transaction that acts on it:
//   - check the binding: an approval's Ceremony.ProposalID and ContentHash
//     against the proposal it acts on (store.ApproveProposal takes the
//     ceremony's ContentHash and compares it with the stored one); a
//     consent acts on Ceremony.Request and Ceremony.Scopes themselves, never
//     on a request or scopes posted alongside the assertion;
//   - consume Ceremony.Nonce(), so the assertion authorises one action only.
//
// A ceremony whose nonce is already consumed fails with store.ErrNonceUsed
// (wrapped) before the assertion is checked, so a replay reads as "already
// used" rather than as a cloned authenticator. An expired ceremony is
// ErrCeremonyExpired, an infrastructure failure (a store query, a stored
// credential that does not decode) is ErrInternal; any other error is a
// failed check.
func (s *Service) FinishAssert(ctx context.Context, token string, want Purpose, credential []byte) (Assertion, error) {
	if want != PurposeLogin && want != PurposeApprove && want != PurposeConsent {
		return Assertion{}, fmt.Errorf("passkey: cannot assert for purpose %q", want)
	}
	c, err := s.signer.Verify(token, want, s.now())
	if err != nil {
		return Assertion{}, err
	}
	used, err := s.db.NonceUsed(ctx, c.Nonce())
	if err != nil {
		return Assertion{}, internal("assert", err)
	}
	if used {
		return Assertion{}, fmt.Errorf("passkey: assert: %w", store.ErrNonceUsed)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return Assertion{}, fmt.Errorf("passkey: assert: %w", err)
	}
	pk, err := s.db.PasskeyByCredentialID(ctx, parsed.RawID)
	if errors.Is(err, store.ErrNotFound) {
		return Assertion{}, ErrUnknownCredential
	}
	if err != nil {
		return Assertion{}, internal("assert", err)
	}
	cred, err := decodeCredential(pk)
	if err != nil {
		return Assertion{}, internal("assert", err)
	}
	// The user holds only this credential, so narrow the signed allow list to
	// it once it is known to have been offered; go-webauthn requires the user
	// to own every allowed credential.
	if !slices.ContainsFunc(c.Session.AllowedCredentialIDs, func(id []byte) bool { return bytes.Equal(id, parsed.RawID) }) {
		return Assertion{}, errors.New("passkey: assert: credential was not offered in this ceremony")
	}
	session := c.Session
	session.AllowedCredentialIDs = [][]byte{parsed.RawID}
	session.UserVerification = protocol.VerificationRequired
	updated, err := s.wa.ValidateLogin(runner{[]webauthn.Credential{cred}}, session, parsed)
	if err != nil {
		return Assertion{}, fmt.Errorf("passkey: assert: %w", err)
	}
	if updated.Authenticator.CloneWarning {
		return Assertion{}, errors.New("passkey: assert: sign count did not increase (possible cloned authenticator)")
	}
	cj, err := json.Marshal(updated)
	if err != nil {
		return Assertion{}, internal("assert", err)
	}
	return Assertion{PasskeyID: pk.ID, CredentialJSON: cj, Ceremony: c}, nil
}
