package oauth

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRegisterDCR(t *testing.T) {
	key := deriveKey(make([]byte, 32), labelDCR)
	req := registration{RedirectURIs: []string{"http://localhost:5/cb", "https://app.example/cb"}, ClientName: "My\x00 App",
		TokenEndpointAuthMethod: "client_secret_basic", GrantTypes: []string{"authorization_code"}}
	id, resp, err := registerDCR(key, req, testSelf, tNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "dcr_") || resp["client_id"] != id || resp["token_endpoint_auth_method"] != "none" ||
		resp["client_id_issued_at"] != tNow.Unix() || resp["client_name"] != "My App" ||
		!reflect.DeepEqual(resp["redirect_uris"], req.RedirectURIs) ||
		!reflect.DeepEqual(resp["grant_types"], []string{"authorization_code", "refresh_token"}) ||
		!reflect.DeepEqual(resp["response_types"], []string{"code"}) {
		t.Fatalf("resp = %v", resp)
	}
	c, err := verifyDCR(key, id)
	if err != nil || c.Name != "My App" || !reflect.DeepEqual(c.RedirectURIs, req.RedirectURIs) || c.Kind != "dcr" || c.Host != "" {
		t.Fatalf("verify: %+v %v", c, err)
	}
	for i := len(id) - 5; i < len(id); i += 2 {
		b := []byte(id)
		b[i] ^= 1
		if _, err := verifyDCR(key, string(b)); !errors.Is(err, errUnknownClient) {
			t.Fatalf("flipped byte %d accepted", i)
		}
	}
	b := []byte(id)
	b[10] ^= 1
	if _, err := verifyDCR(key, string(b)); err == nil {
		t.Fatal("flipped payload accepted")
	}
}

func TestRegisterDCRRefusals(t *testing.T) {
	key := deriveKey(make([]byte, 32), labelDCR)
	ok := "http://localhost/cb"
	long := "https://app.example/" + strings.Repeat("a", 500)
	cases := map[string]registration{
		"none":                {},
		"4 redirects":         {RedirectURIs: []string{ok, ok, ok, ok}},
		"long redirect":       {RedirectURIs: []string{long}},
		"invalid redirect":    {RedirectURIs: []string{"http://evil.example/cb"}},
		"client_credentials":  {RedirectURIs: []string{ok}, GrantTypes: []string{"authorization_code", "client_credentials"}},
		"response_type token": {RedirectURIs: []string{ok}, ResponseTypes: []string{"token"}},
		"redirect on coachd":  {RedirectURIs: []string{testSelf + "/x"}},
	}
	for name, req := range cases {
		if _, _, err := registerDCR(key, req, testSelf, tNow); !errors.Is(err, errRegistration) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, _, err := registerDCR(key, registration{RedirectURIs: []string{ok, ok, ok}}, testSelf, tNow); err != nil {
		t.Errorf("3 redirects: %v", err)
	}
}

func TestDCRKeyIsSeparate(t *testing.T) {
	k := make([]byte, 32)
	id, _, err := registerDCR(deriveKey(k, labelRequest), registration{RedirectURIs: []string{"http://localhost/cb"}}, testSelf, tNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyDCR(deriveKey(k, labelDCR), id); !errors.Is(err, errUnknownClient) {
		t.Fatalf("err = %v", err)
	}
}
