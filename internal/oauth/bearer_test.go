package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/auth"
)

func bearerReq(h string) *http.Request {
	r := httptest.NewRequest("POST", "/mcp", nil)
	if h != "" {
		r.Header.Set("Authorization", h)
	}
	return r
}

func TestVerifyAccess(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	const tok = "cda_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	gid := e.grantWithTokens(t, appID, tok, "cdr_refresh")
	id, err := e.s.VerifyAccess(bearerReq("Bearer " + tok))
	want := auth.Identity{Lane: auth.LanePublic, Subject: "grant:" + itoa(gid), TokenID: "g" + itoa(gid), Scopes: id.Scopes}
	if err != nil || !reflect.DeepEqual(id, want) || len(id.Scopes) == 0 {
		t.Fatalf("valid: %+v %v", id, err)
	}
	if _, err := e.s.VerifyAccess(bearerReq("bearer " + tok)); err != nil {
		t.Fatalf("lowercase scheme: %v", err)
	}
	q := httptest.NewRequest("POST", "/mcp?access_token="+tok, nil)
	for name, tc := range map[string]struct {
		r    *http.Request
		want error
	}{
		"no header":    {bearerReq(""), auth.ErrMissing},
		"basic":        {bearerReq("Basic " + tok), auth.ErrMissing},
		"empty bearer": {bearerReq("Bearer "), auth.ErrMissing},
		"query only":   {q, auth.ErrMissing},
		"private lane": {bearerReq("Bearer cdt_" + tok[4:]), auth.ErrInvalid},
		"unknown":      {bearerReq("Bearer cda_unknown"), auth.ErrInvalid},
		"two tokens":   {bearerReq("Bearer " + tok + " x"), auth.ErrInvalid},
	} {
		if _, err := e.s.VerifyAccess(tc.r); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v want %v", name, err, tc.want)
		}
	}
	*e.now = e.now.Add(2 * time.Hour)
	if _, err := e.s.VerifyAccess(bearerReq("Bearer " + tok)); !errors.Is(err, auth.ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	*e.now = e.now.Add(-2 * time.Hour)
	if err := e.db.RevokeGrant(context.Background(), gid, *e.now, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.VerifyAccess(bearerReq("Bearer " + tok)); !errors.Is(err, auth.ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func TestChallenge(t *testing.T) {
	s, _, _ := newTestOAuth(t, "https://coachd.example.com", false)
	base := `Bearer resource_metadata="https://coachd.example.com/.well-known/oauth-protected-resource/mcp", scope="coach:read coach:write"`
	if got := s.Challenge(auth.ErrMissing); got != base {
		t.Fatalf("missing: %q", got)
	}
	for _, err := range []error{auth.ErrInvalid, auth.ErrExpired, auth.ErrRevoked, errors.New("boom")} {
		if got := s.Challenge(err); got != base+`, error="invalid_token"` {
			t.Fatalf("%v: %q", err, got)
		}
	}
}

func TestVerifyAccessStoreFault(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	e.db.Close()
	_, err := e.s.VerifyAccess(bearerReq("Bearer cda_whatever"))
	if !errors.Is(err, auth.ErrUnavailable) || errors.Is(err, auth.ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}
