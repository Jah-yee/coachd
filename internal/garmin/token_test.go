package garmin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeJWT(payload string) string {
	e := base64.RawURLEncoding.EncodeToString
	return e([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + e([]byte(payload)) + "." + e([]byte("sig"))
}

// exchangeCounts serves body as the token response and counts exchanges;
// the clock is controllable.
func exchangeCounts(t *testing.T, body string, now *time.Time) (*Client, *atomic.Int32) {
	t.Helper()
	var tokens atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokens.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(body))
			return
		}
		w.Write([]byte(`[]`))
	}))
	c.now = func() time.Time { return *now }
	return c, &tokens
}

func TestTokenLifetimeFromJWTExp(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	jwt := fakeJWT(fmt.Sprintf(`{"iat":%d,"exp":%d}`, now.Unix(), now.Unix()+3600))
	c, tokens := exchangeCounts(t, jwt+"\n", &now)
	call := func() {
		if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
			t.Fatal(err)
		}
	}
	call()
	now = now.Add(1799 * time.Second)
	call()
	if tokens.Load() != 1 {
		t.Fatalf("tokens=%d, want cached at 1799s", tokens.Load())
	}
	now = now.Add(2 * time.Second) // 1801s > 3600 - min(1h, 1800)
	call()
	if tokens.Load() != 2 {
		t.Fatalf("tokens=%d, want re-exchange after 1800s", tokens.Load())
	}
}

func TestTokenUndecodablePayloadFallsBackTo24h(t *testing.T) {
	for name, body := range map[string]string{
		"bad base64":   "aaa.!!!.ccc",
		"not json":     fakeJWT(`nope`),
		"no exp":       fakeJWT(`{"iat":1}`),
		"expired exp":  fakeJWT(`{"exp":1}`),
		"two segments": "aaa.bbb",
		"opaque":       "opaquetoken",
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			c, tokens := exchangeCounts(t, body, &now)
			call := func() {
				if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
					t.Fatal(err)
				}
			}
			call()
			now = now.Add(22 * time.Hour) // 86400 - 1h = 23h
			call()
			if tokens.Load() != 1 {
				t.Fatalf("tokens=%d, want cached at 22h", tokens.Load())
			}
			now = now.Add(2 * time.Hour)
			call()
			if tokens.Load() != 2 {
				t.Fatalf("tokens=%d, want re-exchange after 23h", tokens.Load())
			}
		})
	}
}

func TestTokenPaddedPayloadAccepted(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	pl := base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, now.Unix()+3600)))
	c, tokens := exchangeCounts(t, "h."+pl+".s", &now)
	if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
		t.Fatal(err)
	}
	now = now.Add(1801 * time.Second)
	if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Fatalf("tokens=%d, want 2", tokens.Load())
	}
}

func TestTokenJSONShapeStillAccepted(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c, tokens := exchangeCounts(t, `{"access_token":"`+testTok+`","expires_in":7200}`, &now)
	call := func() {
		if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
			t.Fatal(err)
		}
	}
	call()
	now = now.Add(3599 * time.Second)
	call()
	if tokens.Load() != 1 {
		t.Fatalf("tokens=%d", tokens.Load())
	}
	now = now.Add(2 * time.Second)
	call()
	if tokens.Load() != 2 {
		t.Fatalf("tokens=%d", tokens.Load())
	}
}

func TestTokenInvalidBodiesErrorWithoutLeaking(t *testing.T) {
	for name, body := range map[string]string{
		"empty":        "",
		"whitespace":   " \n\t ",
		"inner spaces": "SECRETPART one two",
		"control char": "SECRETPART\x01tail",
		"bad json":     `{"SECRETPART":`,
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			c, _ := exchangeCounts(t, body, &now)
			_, err := c.SleepRange(context.Background(), d1, d2)
			if err == nil {
				t.Fatal("want error")
			}
			if strings.Contains(err.Error(), "SECRETPART") {
				t.Fatalf("error leaks body: %v", err)
			}
		})
	}
}

// keyServer accepts only the keys in ok, answering status for any other, and
// records the key of each data request's token.
func keyServer(t *testing.T, ok map[string]string, status int) (*Client, *[]string) {
	t.Helper()
	var used []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "ApiKey ")
			tok, good := ok[key]
			if !good {
				w.WriteHeader(status)
				return
			}
			w.Write([]byte(`{"access_token":"` + tok + `","expires_in":86400}`))
			return
		}
		used = append(used, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Write([]byte(`[]`))
	}))
	return c, &used
}

func TestUseKeySwitchesKeyAndToken(t *testing.T) {
	c, used := keyServer(t, map[string]string{testKey: "old-token", "new-key": "new-token"}, http.StatusUnauthorized)
	ctx := context.Background()
	if _, err := c.SleepRange(ctx, d1, d2); err != nil {
		t.Fatal(err)
	}
	committed := false
	if err := c.UseKey(ctx, "new-key", func() error { committed = true; return nil }); err != nil || !committed {
		t.Fatalf("UseKey: %v, committed %v", err, committed)
	}
	if _, err := c.SleepRange(ctx, d1, d2); err != nil {
		t.Fatal(err)
	}
	if got := *used; len(got) != 2 || got[0] != "old-token" || got[1] != "new-token" {
		t.Fatalf("data requests used %v", got)
	}
	// The cached token expiring re-exchanges the new key.
	c.mu.Lock()
	c.tok = ""
	c.mu.Unlock()
	if _, err := c.SleepRange(ctx, d1, d2); err != nil || (*used)[2] != "new-token" {
		t.Fatalf("after re-exchange: %v, %v", *used, err)
	}
}

func TestUseKeyFailureKeepsOldKey(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		commit error
		want   func(error) bool
	}{
		"refused":       {http.StatusUnauthorized, nil, func(err error) bool { return errors.Is(err, ErrUnauthorized) }},
		"forbidden":     {http.StatusForbidden, nil, func(err error) bool { return errors.Is(err, ErrUnauthorized) }},
		"rate limited":  {http.StatusTooManyRequests, nil, func(err error) bool { var rl *RateLimitedError; return errors.As(err, &rl) }},
		"server error":  {http.StatusBadGateway, nil, func(err error) bool { var se *StatusError; return errors.As(err, &se) }},
		"commit failed": {0, errors.New("disk full"), func(err error) bool { return err != nil && err.Error() == "disk full" }},
	} {
		t.Run(name, func(t *testing.T) {
			ok := map[string]string{testKey: "old-token"}
			if tc.status == 0 {
				ok["new-key"] = "new-token"
			}
			c, used := keyServer(t, ok, tc.status)
			ctx := context.Background()
			err := c.UseKey(ctx, "new-key", func() error { return tc.commit })
			if !tc.want(err) {
				t.Fatalf("UseKey: %v", err)
			}
			if _, err := c.SleepRange(ctx, d1, d2); err != nil {
				t.Fatal(err)
			}
			if got := *used; len(got) != 1 || got[0] != "old-token" {
				t.Fatalf("data requests used %v, want the old key's token", got)
			}
		})
	}
}
