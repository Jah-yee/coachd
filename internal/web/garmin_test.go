package web

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/garmin"
)

const testGarminKey = "gk_live_0123456789abcdef"

// fakeKey records renewals; err fails them.
type fakeKey struct {
	expires civil.Date
	err     error
	keys    []string
}

func (f *fakeKey) Expires() civil.Date { return f.expires }

func (f *fakeKey) Renew(_ context.Context, key string, expires civil.Date) error {
	f.keys = append(f.keys, key)
	if f.err != nil {
		return f.err
	}
	f.expires = expires
	return nil
}

func garminServer(t *testing.T, daysLeft int) (*testServer, *fakeKey) {
	t.Helper()
	ts := newTestServer(t)
	fk := &fakeKey{expires: ts.today().AddDays(daysLeft)}
	ts.srv.d.GarminKey = fk
	t.Cleanup(func() {
		if strings.Contains(ts.logs.String(), testGarminKey) {
			t.Error("a log line holds the key")
		}
	})
	return ts, fk
}

func renewForm(key, days string) url.Values {
	return url.Values{"key": {key}, "valid_days": {days}}
}

func TestGarminPage(t *testing.T) {
	ts, _ := garminServer(t, 28)
	rec := ts.do(t, "GET", "/garmin", nil, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Valid until "+day(ts.today().AddDays(28))) ||
		!strings.Contains(body, "in 28 days") || !strings.Contains(body, `type="password" name="key" autocomplete="off"`) {
		t.Fatalf("status %d, body %s", rec.Code, body)
	}
	for _, v := range []string{`value="30"`, `value="90"`, `value="180"`, `value="365"`} {
		if !strings.Contains(body, v) {
			t.Errorf("page lacks validity %s", v)
		}
	}
}

func TestGarminRenew(t *testing.T) {
	ts, fk := garminServer(t, 5)
	cookie := ts.enroll(t)
	rec := ts.do(t, "POST", "/garmin", renewForm("  "+testGarminKey+"\n", "90"), cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/garmin?renewed" {
		t.Fatalf("status %d, location %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if len(fk.keys) != 1 || fk.keys[0] != testGarminKey || fk.expires != ts.today().AddDays(90) {
		t.Fatalf("renewals %v until %v", fk.keys, fk.expires)
	}
	rows := ts.audit(t, "garmin_key_renewed")
	if len(rows) != 1 || rows[0].Outcome != "ok" || rows[0].Client != "passkey:1" || rows[0].Actor != "runner" ||
		strings.Contains(rows[0].Target, testGarminKey) {
		t.Fatalf("audit rows %+v", rows)
	}
	if !strings.Contains(ts.logs.String(), `"calendar_date":"`+ts.today().AddDays(90).String()+`"`) {
		t.Fatalf("log lacks the new expiry: %s", ts.logs.String())
	}
	page := ts.do(t, "GET", "/garmin?renewed", nil, cookie).Body.String()
	if !strings.Contains(page, "Renewed") || strings.Contains(page, testGarminKey) {
		t.Fatalf("renewed page: %s", page)
	}
}

func TestGarminRenewNeedsSession(t *testing.T) {
	ts, fk := garminServer(t, 5)
	ts.enroll(t)
	rec := ts.do(t, "POST", "/garmin", renewForm(testGarminKey, "90"), nil)
	if rec.Code != http.StatusUnauthorized || len(fk.keys) != 0 {
		t.Fatalf("status %d, renewals %v", rec.Code, fk.keys)
	}
}

func TestGarminRenewFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		status  int
		msg     string
		outcome string
	}{
		"refused":      {garmin.ErrUnauthorized, http.StatusBadRequest, msgKeyRefused, "refused"},
		"rate limited": {&garmin.RateLimitedError{}, http.StatusServiceUnavailable, msgKeyRateLimited, "rate_limited"},
		"unreachable":  {&garmin.StatusError{Endpoint: "token"}, http.StatusBadGateway, msgKeyUnreachable, "unreachable"},
		"garmin 5xx":   {&garmin.StatusError{Endpoint: "token", Status: 502}, http.StatusBadGateway, msgKeyFailed, "error"},
		"write failed": {errors.New("disk full"), http.StatusBadGateway, msgKeyFailed, "error"},
	} {
		t.Run(name, func(t *testing.T) {
			ts, fk := garminServer(t, 5)
			fk.err = tc.err
			before := fk.expires
			rec := ts.do(t, "POST", "/garmin", renewForm(testGarminKey, "90"), ts.enroll(t))
			body := rec.Body.String()
			if rec.Code != tc.status || !strings.Contains(body, html.EscapeString(tc.msg)) {
				t.Fatalf("status %d, body %s", rec.Code, body)
			}
			if strings.Contains(body, testGarminKey) {
				t.Fatal("the page shows the key back")
			}
			if fk.expires != before {
				t.Fatal("expiry changed")
			}
			if rows := ts.audit(t, "garmin_key_renewed"); len(rows) != 1 || rows[0].Outcome != tc.outcome {
				t.Fatalf("audit rows %+v", rows)
			}
		})
	}
}

func TestGarminRenewInput(t *testing.T) {
	for name, tc := range map[string]struct {
		form url.Values
		msg  string
	}{
		"no key":       {renewForm("  ", "90"), msgKeyMissing},
		"inner space":  {renewForm("abc def", "90"), msgKeyMalformed},
		"control":      {renewForm("abc\x01def", "90"), msgKeyMalformed},
		"too long":     {renewForm(strings.Repeat("k", maxKeyBytes+1), "90"), msgKeyMalformed},
		"odd validity": {renewForm(testGarminKey, "7"), msgKeyValidity},
		"no validity":  {url.Values{"key": {testGarminKey}}, msgKeyValidity},
		"not a number": {renewForm(testGarminKey, "ninety"), msgKeyValidity},
	} {
		t.Run(name, func(t *testing.T) {
			ts, fk := garminServer(t, 5)
			rec := ts.do(t, "POST", "/garmin", tc.form, ts.enroll(t))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), html.EscapeString(tc.msg)) {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body)
			}
			if len(fk.keys) != 0 || len(ts.audit(t, "garmin_key_renewed")) != 0 {
				t.Fatal("invalid input reached Garmin or the audit log")
			}
		})
	}
}

func TestGarminUnavailable(t *testing.T) {
	ts := newTestServer(t)
	if body := ts.do(t, "GET", "/garmin", nil, nil).Body.String(); !strings.Contains(body, "not available") {
		t.Fatalf("page without renewal: %s", body)
	}
	if rec := ts.do(t, "POST", "/garmin", renewForm(testGarminKey, "90"), ts.enroll(t)); rec.Code != http.StatusNotFound {
		t.Fatalf("POST without renewal: %d", rec.Code)
	}
	if strings.Contains(ts.do(t, "GET", "/", nil, nil).Body.String(), `href="/garmin"`) {
		t.Fatal("home links the key page without renewal")
	}
}

func TestHomeGarminKey(t *testing.T) {
	ts, fk := garminServer(t, 30)
	home := func() string { return ts.do(t, "GET", "/", nil, nil).Body.String() }
	if h := home(); !strings.Contains(h, `href="/garmin"`) || strings.Contains(h, "renew</span>") {
		t.Fatalf("home with 30 days left: %s", h)
	}
	fk.expires = ts.today().AddDays(keySoonDays)
	if h := home(); !strings.Contains(h, "in 14 days: renew") {
		t.Fatalf("home with 14 days left: %s", h)
	}
	fk.expires = ts.today().AddDays(-2)
	if h := home(); !strings.Contains(h, "Expired") {
		t.Fatalf("home after expiry: %s", h)
	}
}
