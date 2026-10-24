package auth

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testAUD = "aud-123"

type jwtEnv struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newJWTEnv(t *testing.T) *jwtEnv {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn-cgi/access/certs", func(w http.ResponseWriter, r *http.Request) {
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig", "n": n, "e": e}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &jwtEnv{srv: srv, key: key}
}

func (e *jwtEnv) mint(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	signing := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, e.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (e *jwtEnv) claims(email string, exp time.Time) map[string]any {
	return map[string]any{"iss": e.srv.URL, "aud": []string{testAUD}, "email": email, "sub": "u1",
		"iat": time.Now().Add(-time.Hour).Unix(), "exp": exp.Unix()}
}

func reqWithJWT(jwt string) *http.Request {
	r := httptest.NewRequest("GET", "/mcp", nil)
	if jwt != "" {
		r.Header.Set("Cf-Access-Jwt-Assertion", jwt)
	}
	return r
}

func TestAccessVerifierValid(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "Me@Example.com")
	id, err := v.Verify(reqWithJWT(env.mint(t, env.claims("me@example.com", time.Now().Add(time.Hour)))))
	if err != nil {
		t.Fatal(err)
	}
	if id.Lane != LanePublic || id.Subject != "me@example.com" || !id.Has(ScopeRead) || !id.Has(ScopeWrite) {
		t.Fatalf("%+v", id)
	}
}

func TestAccessVerifierEmailCaseInsensitive(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "me@example.com")
	id, err := v.Verify(reqWithJWT(env.mint(t, env.claims("ME@EXAMPLE.COM", time.Now().Add(time.Hour)))))
	if err != nil || id.Subject != "me@example.com" {
		t.Fatalf("%+v %v", id, err)
	}
}

func TestAccessVerifierWrongAud(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, "other-aud", "me@example.com")
	jwt := env.mint(t, env.claims("me@example.com", time.Now().Add(time.Hour)))
	if _, err := v.Verify(reqWithJWT(jwt)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestAccessVerifierExpired(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "me@example.com")
	jwt := env.mint(t, env.claims("me@example.com", time.Now().Add(-time.Minute)))
	_, err := v.Verify(reqWithJWT(jwt))
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), jwt) {
		t.Fatal("error leaks jwt")
	}
}

func TestAccessVerifierWrongEmail(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "me@example.com")
	_, err := v.Verify(reqWithJWT(env.mint(t, env.claims("evil@example.com", time.Now().Add(time.Hour)))))
	if !errors.Is(err, ErrForbiddenEmail) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "evil@") || strings.Contains(err.Error(), "me@") {
		t.Fatal("error leaks email")
	}
	c := env.claims("", time.Now().Add(time.Hour))
	delete(c, "email")
	if _, err := v.Verify(reqWithJWT(env.mint(t, c))); !errors.Is(err, ErrForbiddenEmail) {
		t.Fatalf("missing email: %v", err)
	}
}

func TestAccessVerifierMissingAndGarbage(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "me@example.com")
	if _, err := v.Verify(reqWithJWT("")); !errors.Is(err, ErrMissing) {
		t.Fatalf("err %v", err)
	}
	if _, err := v.Verify(reqWithJWT("not.a.jwt")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestAccessVerifierBadSignature(t *testing.T) {
	env := newJWTEnv(t)
	other := newJWTEnv(t) // different key, same kid
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "me@example.com")
	jwt := other.mint(t, env.claims("me@example.com", time.Now().Add(time.Hour)))
	if _, err := v.Verify(reqWithJWT(jwt)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestAccessVerifierNormalizesTeamDomain(t *testing.T) {
	// Trailing slash on the configured domain must not break the iss match.
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL+"/", testAUD, "me@example.com")
	if _, err := v.Verify(reqWithJWT(env.mint(t, env.claims("me@example.com", time.Now().Add(time.Hour))))); err != nil {
		t.Fatal(err)
	}
	// Bare host gains https:// (string-level, no network).
	for in, want := range map[string]string{
		"team.cloudflareaccess.com":   "https://team.cloudflareaccess.com",
		"team.cloudflareaccess.com/":  "https://team.cloudflareaccess.com",
		" team.cloudflareaccess.com ": "https://team.cloudflareaccess.com",
		"http://127.0.0.1:1234":       "http://127.0.0.1:1234",
		"https://t.example.com//":     "https://t.example.com",
	} {
		if got := normalizeTeamDomain(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccessVerifierWrongIssuer(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "me@example.com")
	c := env.claims("me@example.com", time.Now().Add(time.Hour))
	c["iss"] = "https://evil.example.com"
	if _, err := v.Verify(reqWithJWT(env.mint(t, c))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestAccessVerifierRejectsNoneAndHS256(t *testing.T) {
	env := newJWTEnv(t)
	v := NewAccessVerifier(context.Background(), env.srv.URL, testAUD, "me@example.com")
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	claims := enc(env.claims("me@example.com", time.Now().Add(time.Hour)))

	none := enc(map[string]string{"alg": "none", "typ": "JWT"}) + "." + claims + "."
	if _, err := v.Verify(reqWithJWT(none)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("alg none: %v", err)
	}

	signing := enc(map[string]string{"alg": "HS256", "kid": "k1", "typ": "JWT"}) + "." + claims
	mac := hmac.New(sha256.New, []byte("any-key"))
	mac.Write([]byte(signing))
	hs := signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := v.Verify(reqWithJWT(hs)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("HS256: %v", err)
	}
}
