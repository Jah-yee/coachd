package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	dcrPrefix       = "dcr_"
	maxDCRRedirects = 3
	maxDCRRedirect  = 512
)

var b64 = base64.RawURLEncoding.Strict()

// errRegistration means a registration request broke a rule of spec §5.2
// (RFC 7591 invalid_client_metadata). It never carries a submitted value.
var errRegistration = errors.New("oauth: invalid client metadata")

// errRedirectURI is an errRegistration caused by a redirect URI (RFC 7591
// invalid_redirect_uri).
var errRedirectURI = fmt.Errorf("%w: redirect_uris", errRegistration)

type registration struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// dcrBlob is the signed payload of a DCR client_id.
type dcrBlob struct {
	R []string `json:"r"`
	N string   `json:"n,omitempty"`
	I int64    `json:"iat"`
}

// registerDCR validates req and returns the stateless client_id, an
// HMAC-signed blob of the redirects and name, and the RFC 7591 response.
// Nothing is stored: the client row appears at first consent.
func registerDCR(key []byte, req registration, self string, now time.Time) (string, map[string]any, error) {
	if len(key) < sha256.Size || len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > maxDCRRedirects {
		return "", nil, errRegistration
	}
	for _, r := range req.RedirectURIs {
		if len(r) > maxDCRRedirect || !validRedirectURI(r, self) {
			return "", nil, errRedirectURI
		}
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return "", nil, errRegistration
		}
	}
	for _, g := range req.ResponseTypes {
		if g != "code" {
			return "", nil, errRegistration
		}
	}
	name := ""
	if req.ClientName != "" {
		name = displayName(req.ClientName)
	}
	payload, err := json.Marshal(dcrBlob{R: req.RedirectURIs, N: name, I: now.Unix()})
	if err != nil {
		return "", nil, errRegistration
	}
	id := dcrPrefix + b64.EncodeToString(payload) + "." + b64.EncodeToString(blobMAC(key, payload))
	resp := map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        now.Unix(),
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if name != "" {
		resp["client_name"] = name
	}
	return id, resp, nil
}

// verifyDCR checks a DCR client_id. The MAC is compared (hmac.Equal) before
// the payload is decoded as JSON. The RowID is left for the resolver.
func verifyDCR(key []byte, clientID string) (client, error) {
	rest, ok := strings.CutPrefix(clientID, dcrPrefix)
	if !ok || len(key) < sha256.Size {
		return client{}, errUnknownClient
	}
	p, m, ok := strings.Cut(rest, ".")
	if !ok {
		return client{}, errUnknownClient
	}
	payload, err := b64.DecodeString(p)
	if err != nil {
		return client{}, errUnknownClient
	}
	sum, err := b64.DecodeString(m)
	if err != nil || !hmac.Equal(sum, blobMAC(key, payload)) {
		return client{}, errUnknownClient
	}
	var b dcrBlob
	if err := json.Unmarshal(payload, &b); err != nil || len(b.R) == 0 || slices.Contains(b.R, "") {
		return client{}, errUnknownClient
	}
	return client{ClientID: clientID, Kind: "dcr", Name: displayName(b.N), RedirectURIs: b.R}, nil
}
