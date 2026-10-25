package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

const msgBadCode = "no pending pairing with that code; codes expire after 10 minutes"

func (ts *testServer) newPairing(t *testing.T, id, code string) {
	t.Helper()
	now := ts.clock.Now()
	if err := ts.db.CreatePairing(context.Background(),
		store.Pairing{ID: id, Code: code, Model: "fenix7spro", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, 5); err != nil {
		t.Fatal(err)
	}
}

func TestDevicesPairFlow(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	id := strings.Repeat("ab", 16)
	ts.newPairing(t, id, "123456")
	rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {" 123456 "}, "label": {" Fenix "}}, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/devices" {
		t.Fatalf("pair: status %d, Location %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	hash := sha256.Sum256([]byte("tok"))
	st, devID, err := ts.db.ClaimPairing(context.Background(), id, hash[:], ts.clock.Now())
	if err != nil || st != "paired" || devID == 0 {
		t.Fatalf("claim = %q %d %v", st, devID, err)
	}
	page := ts.do(t, "GET", "/devices", nil, cookie).Body.String()
	for _, want := range []string{"Fenix", "fenix7spro", `action="/devices/` + "1" + `/revoke"`} {
		if !strings.Contains(page, want) {
			t.Errorf("devices page lacks %q: %s", want, page)
		}
	}
	rows := ts.audit(t, "device_pairing_confirmed")
	if len(rows) != 1 || rows[0].Lane != "public" || rows[0].Actor != "runner" || rows[0].Client != "passkey:1" {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestDevicesDefaultLabel(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	id := strings.Repeat("cd", 16)
	ts.newPairing(t, id, "654321")
	rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {"654321"}, "label": {"  "}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	hash := sha256.Sum256([]byte("tok2"))
	if _, _, err := ts.db.ClaimPairing(context.Background(), id, hash[:], ts.clock.Now()); err != nil {
		t.Fatal(err)
	}
	ds, _ := ts.db.ListDevices(context.Background())
	if len(ds) != 1 || ds[0].Label != "watch 2026-10-23" {
		t.Fatalf("devices = %+v", ds)
	}
}

func TestDevicesWritesNeedSession(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	id := strings.Repeat("ab", 16)
	ts.newPairing(t, id, "123456")
	rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {"x"}}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pair without session: %d", rec.Code)
	}
	hash := sha256.Sum256([]byte("tok"))
	if st, _, _ := ts.db.ClaimPairing(context.Background(), id, hash[:], ts.clock.Now()); st != "pending" {
		t.Fatalf("pairing was confirmed without a session: %s", st)
	}
	ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {"x"}}, cookie)
	if st, _, _ := ts.db.ClaimPairing(context.Background(), id, hash[:], ts.clock.Now()); st != "paired" {
		t.Fatalf("claim = %s", st)
	}
	rec = ts.do(t, "POST", "/devices/1/revoke", url.Values{}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoke without session: %d", rec.Code)
	}
	if _, err := ts.db.DeviceByTokenHash(context.Background(), hash[:]); err != nil {
		t.Fatalf("device changed: %v", err)
	}
	if n := len(ts.audit(t, "device_revoked")); n != 0 {
		t.Fatalf("device_revoked rows = %d", n)
	}
}

func TestDevicesBadCode(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	ts.newPairing(t, strings.Repeat("ab", 16), "123456")
	for _, code := range []string{"999999", "12345", "abcdef", "1234567", ""} {
		rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {code}, "label": {"x"}}, cookie)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), msgBadCode) {
			t.Errorf("code %q: status %d, body %s", code, rec.Code, rec.Body)
		}
	}
	// the real pairing is untouched
	rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {"x"}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("real code: %d", rec.Code)
	}
}

func TestDevicesLabelError(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	ts.newPairing(t, strings.Repeat("ab", 16), "123456")
	rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {strings.Repeat("x", 41)}}, cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "41 characters exceeds the 40 limit") {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	// Labels are single-line.
	for _, label := range []string{"Fen\tix", "Fen\nix"} {
		rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {label}}, cookie)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "label: must be a single line (no tab or newline)") {
			t.Errorf("label %q: status %d, body %s", label, rec.Code, rec.Body)
		}
	}
	if rows := ts.audit(t, "device_pairing_confirmed"); len(rows) != 0 {
		t.Fatalf("a rejected label confirmed the pairing: %+v", rows)
	}
}

func TestRevokeDevice(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	id := strings.Repeat("ab", 16)
	ts.newPairing(t, id, "123456")
	ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {"Fenix"}}, cookie)
	hash := sha256.Sum256([]byte("tok"))
	if _, _, err := ts.db.ClaimPairing(context.Background(), id, hash[:], ts.clock.Now()); err != nil {
		t.Fatal(err)
	}
	rec := ts.do(t, "POST", "/devices/1/revoke", url.Values{}, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/devices" {
		t.Fatalf("revoke: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(ts.do(t, "GET", "/devices", nil, cookie).Body.String(), "revoked") {
		t.Error("page lacks revoked")
	}
	if _, err := ts.db.DeviceByTokenHash(context.Background(), hash[:]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeviceByTokenHash = %v", err)
	}
	if rows := ts.audit(t, "device_revoked"); len(rows) != 1 || rows[0].Client != "passkey:1" || rows[0].Actor != "runner" {
		t.Fatalf("audit = %+v", rows)
	}
	for _, p := range []string{"/devices/1/revoke", "/devices/99/revoke", "/devices/x/revoke"} {
		if rec := ts.do(t, "POST", p, url.Values{}, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
}

func TestDevicesEscapeLabel(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	id := strings.Repeat("ab", 16)
	now := ts.clock.Now()
	if err := ts.db.CreatePairing(context.Background(),
		store.Pairing{ID: id, Code: "123456", Model: "<b>m</b>", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, 5); err != nil {
		t.Fatal(err)
	}
	ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {"<script>alert(1)</script>"}}, cookie)
	hash := sha256.Sum256([]byte("tok"))
	if _, _, err := ts.db.ClaimPairing(context.Background(), id, hash[:], now); err != nil {
		t.Fatal(err)
	}
	page := ts.do(t, "GET", "/devices", nil, cookie).Body.String()
	if strings.Contains(page, "<script>alert") || strings.Contains(page, "<b>m</b>") ||
		!strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("label not escaped: %s", page)
	}
}

func TestWithdrawDelivering(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	for _, st := range []store.ProposalStatus{store.ProposalDelivering, store.ProposalDeliveryFailed} {
		p := ts.addProposal(t, "Tempo "+string(st), ts.today().AddDays(1), st, "")
		page := ts.page(t, p.ID, cookie)
		if !strings.Contains(page, `action="/p/`+p.ID+`/withdraw"`) {
			t.Errorf("%s page lacks withdraw", st)
		}
		rec := ts.do(t, "POST", "/p/"+p.ID+"/withdraw", url.Values{}, cookie)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s withdraw: %d %s", st, rec.Code, rec.Body)
		}
		if got := ts.status(t, p.ID); got != store.ProposalWithdrawn {
			t.Fatalf("status = %s", got)
		}
		if !strings.Contains(ts.page(t, p.ID, cookie), watchHint) {
			t.Errorf("%s: withdrawn page lacks the watch hint", st)
		}
	}
	// delivery states have a status line
	p := ts.addProposal(t, "Easy", ts.today().AddDays(2), store.ProposalDeliveryFailed, "")
	if !strings.Contains(ts.page(t, p.ID, cookie), "delivery to the watch failed") {
		t.Error("delivery_failed page lacks its status line")
	}
}

// A manual download of a workout on its way to the watch (or whose delivery
// failed) delivers it; a later watch ack is a no-op.
func TestFITDownloadFromDeliveryStates(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	ctx := context.Background()
	now := ts.clock.Now()
	pairingID := strings.Repeat("ab", 16)
	ts.newPairing(t, pairingID, "123456")
	if _, err := ts.db.ConfirmPairing(ctx, "123456", "Fenix", now, "cli", "cli", "cli"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("tok"))
	_, devID, err := ts.db.ClaimPairing(ctx, pairingID, hash[:], now)
	if err != nil {
		t.Fatal(err)
	}
	going := ts.addProposal(t, "Tempo", ts.today().AddDays(1), store.ProposalApproved, "")
	if _, err := ts.db.RecordFetch(ctx, going.ID, devID, now); err != nil {
		t.Fatal(err)
	}
	failed := ts.addProposal(t, "Easy", ts.today().AddDays(2), store.ProposalDeliveryFailed, "")
	for _, c := range []struct {
		p  store.Proposal
		st store.ProposalStatus
	}{{going, store.ProposalDelivering}, {failed, store.ProposalDeliveryFailed}} {
		if got := ts.status(t, c.p.ID); got != c.st {
			t.Fatalf("%s: status = %s before the download", c.p.Name, got)
		}
		if page := ts.page(t, c.p.ID, cookie); !strings.Contains(page, `action="/p/`+c.p.ID+`/fit"`) {
			t.Errorf("%s page lacks the download button", c.st)
		}
		rec := ts.do(t, "POST", "/p/"+c.p.ID+"/fit", url.Values{}, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s download: status %d, body %s", c.st, rec.Code, rec.Body)
		}
		if _, wk, _ := decodeFIT(t, rec.Body.Bytes()); wk.WktName != c.p.Name {
			t.Errorf("%s: workout %q", c.st, wk.WktName)
		}
		if got := ts.status(t, c.p.ID); got != store.ProposalDelivered {
			t.Fatalf("%s: status = %s after the download", c.st, got)
		}
	}
	rows := ts.audit(t, "proposal_delivered")
	if len(rows) != 2 {
		t.Fatalf("proposal_delivered rows = %+v", rows)
	}
	for _, r := range rows {
		if r.Actor != "runner" || r.Client != "passkey:1" || r.Lane != "public" {
			t.Errorf("proposal_delivered row = %+v", r)
		}
	}
	if d := ts.deliveries(t); d["delivered/fit"] != 2 || len(d) != 1 {
		t.Fatalf("deliveries = %v", d)
	}
	// The watch acks the copy it fetched earlier: no transition, no new row.
	p, err := ts.db.AckDelivery(ctx, going.ID, devID, ts.clock.Now())
	if err != nil || p.Status != store.ProposalDelivered {
		t.Fatalf("late ack = %s, %v", p.Status, err)
	}
	if n := len(ts.audit(t, "proposal_delivered")); n != 2 {
		t.Fatalf("proposal_delivered rows = %d after the late ack", n)
	}
}

// Pending pairings show the model and age, never the code: the runner must
// read it from the watch.
func TestDevicesListsPendingPairings(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	ctx := context.Background()
	if page := ts.do(t, "GET", "/devices", nil, cookie).Body.String(); strings.Contains(page, "Waiting for confirmation") {
		t.Errorf("devices page lists pending pairings without any: %s", page)
	}
	first := strings.Repeat("ab", 16)
	ts.newPairing(t, first, "123456")
	ts.clock.Add(2 * time.Minute)
	second := strings.Repeat("cd", 16)
	now := ts.clock.Now()
	if err := ts.db.CreatePairing(ctx, store.Pairing{ID: second, Code: "987654", Model: "<b>fenix</b>",
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, 5); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*http.Cookie{cookie, nil} {
		page := ts.do(t, "GET", "/devices", nil, c).Body.String()
		for _, want := range []string{"Waiting for confirmation", "fenix7spro", "started 2 min ago", "&lt;b&gt;fenix&lt;/b&gt;", "started just now"} {
			if !strings.Contains(page, want) {
				t.Errorf("devices page lacks %q: %s", want, page)
			}
		}
		if strings.Index(page, "&lt;b&gt;fenix") > strings.Index(page, "fenix7spro") {
			t.Error("pending pairings are not newest first")
		}
		for _, secret := range []string{"123456", "987654", first, second, first[:8], second[:8], "<b>fenix"} {
			if strings.Contains(page, secret) {
				t.Errorf("devices page shows %q", secret)
			}
		}
	}
	// A confirmed pairing leaves the list.
	if rec := ts.do(t, "POST", "/devices/pair", url.Values{"code": {"123456"}, "label": {"Fenix"}}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("pair: %d %s", rec.Code, rec.Body)
	}
	if page := ts.do(t, "GET", "/devices", nil, cookie).Body.String(); strings.Contains(page, "fenix7spro") {
		t.Errorf("confirmed pairing still listed: %s", page)
	}
}
