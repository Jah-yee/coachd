package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
	"github.com/stefiix92/coachd/internal/zwiftworkout"
)

// ride is a cycling workout Zwift can take: time steps only, with power.
func ride(date civil.Date) workout.Workout {
	return workout.Workout{Name: "Sweet spot", Sport: "cycling", Date: date.String(), Steps: []workout.Step{
		{StepBase: workout.StepBase{Kind: "warmup", Duration: &workout.Duration{Type: "time", TimeS: ip(600)},
			Target: &workout.Target{Type: "power", LowPctFTP: ip(50), HighPctFTP: ip(60)}}},
		{StepBase: workout.StepBase{Kind: "active", Duration: &workout.Duration{Type: "time", TimeS: ip(1200)},
			Target: &workout.Target{Type: "power", LowPctFTP: ip(88), HighPctFTP: ip(92)}}},
	}}
}

func spanFormat(t *testing.T, ts *testServer) []string {
	t.Helper()
	var out []string
	for _, s := range ts.spansNamed("proposal.deliver") {
		for _, kv := range s.Attributes() {
			if kv.Key == "format" {
				out = append(out, kv.Value.AsString())
			}
		}
	}
	return out
}

func TestZwoDownload(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	w := ride(ts.today().AddDays(1))
	p := ts.propose(t, w, proposeOpts{status: store.ProposalApproved, traced: true})

	page := ts.page(t, p.ID, cookie)
	if !strings.Contains(page, `action="/p/`+p.ID+`/zwo"`) || !strings.Contains(page, "Download for Zwift") {
		t.Fatalf("page lacks the Zwift download: %s", page)
	}
	if strings.Contains(page, "Not exportable to Zwift") {
		t.Error("exportable workout shows the reason line")
	}

	rec := ts.do(t, "POST", "/p/"+p.ID+"/zwo", url.Values{}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: status %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/xml" {
		t.Errorf("Content-Type = %q", ct)
	}
	wantCD := `attachment; filename="coachd-2026-10-24-` + p.ID[:8] + `.zwo"`
	if cd := rec.Header().Get("Content-Disposition"); cd != wantCD {
		t.Errorf("Content-Disposition = %q, want %q", cd, wantCD)
	}
	checkPageHeaders(t, "POST /p/{id}/zwo", rec.Header(), "no-store")
	want, err := zwiftworkout.Encode(w)
	if err != nil {
		t.Fatal(err)
	}
	first := rec.Body.Bytes()
	if !bytes.Equal(first, want) {
		t.Fatalf("body is not the encoded workout:\n%s", first)
	}
	if got := ts.status(t, p.ID); got != store.ProposalDelivered {
		t.Fatalf("status = %s", got)
	}
	rows := ts.audit(t, "proposal_delivered")
	if len(rows) != 1 || rows[0].Target != p.ID || rows[0].Client != "passkey:1" || rows[0].Lane != "public" || rows[0].Actor != "runner" {
		t.Fatalf("proposal_delivered rows = %+v", rows)
	}

	rec = ts.do(t, "POST", "/p/"+p.ID+"/zwo", url.Values{}, cookie)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), first) {
		t.Fatalf("second download: status %d, same bytes %v", rec.Code, bytes.Equal(rec.Body.Bytes(), first))
	}
	if n := len(ts.audit(t, "proposal_delivered")); n != 1 {
		t.Fatalf("proposal_delivered rows = %d after a second download", n)
	}
	if d := ts.deliveries(t); d["delivered/zwo"] != 1 || d["redownload/zwo"] != 1 || len(d) != 2 {
		t.Fatalf("deliveries = %v", d)
	}
	if f := spanFormat(t, ts); len(f) != 2 || f[0] != "zwo" || f[1] != "zwo" {
		t.Fatalf("span formats = %v", f)
	}
	for _, s := range ts.spansNamed("proposal.deliver") {
		checkProposalSpan(t, s, p.ID)
	}
}

// Review focus 3: the first download of either file delivers; the other
// format is then a redownload.
func TestZwoThenFITDeliversOnce(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.propose(t, ride(ts.today().AddDays(1)), proposeOpts{status: store.ProposalApproved})
	for _, path := range []string{"/zwo", "/fit"} {
		if rec := ts.do(t, "POST", "/p/"+p.ID+path, url.Values{}, cookie); rec.Code != http.StatusOK {
			t.Fatalf("POST %s: status %d, body %s", path, rec.Code, rec.Body)
		}
	}
	if n := len(ts.audit(t, "proposal_delivered")); n != 1 {
		t.Fatalf("proposal_delivered rows = %d", n)
	}
	if d := ts.deliveries(t); d["delivered/zwo"] != 1 || d["redownload/fit"] != 1 || len(d) != 2 {
		t.Fatalf("deliveries = %v", d)
	}
}

// Review focus 4: a cycling workout Zwift cannot take shows the reason and
// refuses the download without a transition; other sports show neither.
func TestZwoNotExportable(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	w := ride(ts.today().AddDays(1))
	w.Steps = append(w.Steps, workout.Step{StepBase: workout.StepBase{Kind: "cooldown",
		Duration: &workout.Duration{Type: "distance", DistanceM: ip(5000)}}})
	p := ts.propose(t, w, proposeOpts{status: store.ProposalApproved})

	page := ts.page(t, p.ID, cookie)
	if !strings.Contains(page, "Not exportable to Zwift: steps[2].duration: distance durations have no Zwift equivalent") {
		t.Errorf("page lacks the reason line: %s", page)
	}
	if strings.Contains(page, "/zwo") {
		t.Error("page offers the Zwift download")
	}
	rec := ts.do(t, "POST", "/p/"+p.ID+"/zwo", url.Values{}, cookie)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not exportable to Zwift") {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, p.ID); got != store.ProposalApproved {
		t.Fatalf("status = %s", got)
	}
	if rows := ts.audit(t, "proposal_delivered"); len(rows) != 0 {
		t.Fatalf("proposal_delivered rows = %+v", rows)
	}

	run := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalApproved, "")
	page = ts.page(t, run.ID, cookie)
	if strings.Contains(page, "/zwo") || strings.Contains(page, "Zwift") {
		t.Errorf("running page mentions Zwift: %s", page)
	}
	if rec := ts.do(t, "POST", "/p/"+run.ID+"/zwo", url.Values{}, cookie); rec.Code != http.StatusConflict {
		t.Errorf("running zwo: status %d", rec.Code)
	}
	if d := ts.deliveries(t); len(d) != 0 {
		t.Fatalf("deliveries = %v", d)
	}
}

func TestZwoPendingConflict(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	for _, st := range []store.ProposalStatus{store.ProposalPending, store.ProposalDeclined, store.ProposalWithdrawn} {
		p := ts.propose(t, ride(ts.today().AddDays(1)), proposeOpts{status: st})
		rec := ts.do(t, "POST", "/p/"+p.ID+"/zwo", url.Values{}, cookie)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "this proposal is "+string(st)) {
			t.Errorf("%s: status %d, body %s", st, rec.Code, rec.Body)
		}
		if got := ts.status(t, p.ID); got != st {
			t.Errorf("%s: status changed to %s", st, got)
		}
	}
}

func TestZwoIntegrity(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.propose(t, ride(ts.today().AddDays(1)), proposeOpts{status: store.ProposalApproved})
	changed := strings.Replace(string(p.ContentJSON), `"time_s":1200`, `"time_s":3600`, 1)
	if changed == string(p.ContentJSON) {
		t.Fatal("nothing to tamper with")
	}
	ts.rawExec(t, `UPDATE proposals SET content_json=? WHERE id=?`, changed, p.ID)
	if page := ts.page(t, p.ID, cookie); strings.Contains(page, "/zwo") {
		t.Error("page offers the Zwift download of tampered content")
	}
	rec := ts.do(t, "POST", "/p/"+p.ID+"/zwo", url.Values{}, cookie)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "stored content does not match its hash; download disabled") {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, p.ID); got != store.ProposalApproved {
		t.Fatalf("status = %s", got)
	}
}

func TestZwoCrossSiteRejected(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.propose(t, ride(ts.today().AddDays(1)), proposeOpts{status: store.ProposalApproved})
	req := httptest.NewRequest("POST", testOrigin+"/p/"+p.ID+"/zwo", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(cookie)
	if rec := ts.serve(req); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site download: status %d, want 403", rec.Code)
	}
	if got := ts.status(t, p.ID); got != store.ProposalApproved {
		t.Fatalf("status = %s", got)
	}
}
