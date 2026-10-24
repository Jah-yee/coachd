package oauth

import (
	"encoding/json"
	"net/http"
)

var (
	scopesProtected = []string{"coach:read", "coach:write"}
	scopesAS        = []string{"coach:read", "coach:write", "offline_access"}
)

// protectedResource is the RFC 9728 document.
type protectedResource struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// authServerMetadata is the RFC 8414 document (spec §5.1).
type authServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RevocationEndpoint                         string   `json:"revocation_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint,omitempty"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethodsSupported     []string `json:"revocation_endpoint_auth_methods_supported"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

// buildMetadata renders both documents once, from the configured public URL.
func buildMetadata(self string, dcr bool) (prm, as []byte, err error) {
	prm, err = json.Marshal(protectedResource{
		Resource:               self + "/mcp",
		AuthorizationServers:   []string{self},
		ScopesSupported:        scopesProtected,
		BearerMethodsSupported: []string{"header"},
	})
	if err != nil {
		return nil, nil, err
	}
	m := authServerMetadata{
		Issuer:                                     self,
		AuthorizationEndpoint:                      self + "/oauth/authorize",
		TokenEndpoint:                              self + "/oauth/token",
		RevocationEndpoint:                         self + "/oauth/revoke",
		ResponseTypesSupported:                     []string{"code"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		RevocationEndpointAuthMethodsSupported:     []string{"none"},
		ScopesSupported:                            scopesAS,
		ClientIDMetadataDocumentSupported:          true,
		AuthorizationResponseIssParameterSupported: true,
	}
	if dcr {
		m.RegistrationEndpoint = self + "/oauth/register"
	}
	as, err = json.Marshal(m)
	return prm, as, err
}

func serveDoc(doc []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(doc)
	}
}
