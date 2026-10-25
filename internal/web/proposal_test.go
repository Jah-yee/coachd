package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/passkey/passkeytest"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	watchHint = "If you copied it to the watch, delete it there too. The watch app removes its own copy on the next sync."
	// The trace context stored with a traced test proposal.
	testTraceID = "0102030405060708090a0b0c0d0e0f10"
	testSpanID  = "0102030405060708"
)

// proposeOpts are the optional fields of a test proposal.
type proposeOpts struct {
	status    store.ProposalStatus // default pending
	rationale string               // default "base building"
	warnings  string               // stored warnings JSON; default "[]"
	replaces  string               // id of the proposal it replaces
	traced    bool                 // store testTraceID and testSpanID
}

// propose stores w as a proposal written by an agent, as propose_workout does.
func (ts *testServer) propose(t *testing.T, w workout.Workout, o proposeOpts) store.Proposal {
	t.Helper()
	canon, err := workout.CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	date, err := civil.Parse(w.Date)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	if o.status == "" {
		o.status = store.ProposalPending
	}
	if o.rationale == "" {
		o.rationale = "base building"
	}
	if o.warnings == "" {
		o.warnings = "[]"
	}
	now := ts.clock.Now()
	p := store.Proposal{ID: hex.EncodeToString(id), Date: date, Sport: store.Sport(w.Sport), Name: w.Name,
		ContentJSON: canon, ContentHash: workout.ContentHash(canon), Rationale: o.rationale,
		WarningsJSON: []byte(o.warnings), Status: o.status, StatusAt: now, CreatedAt: now,
		CreatedLane: "private", CreatedBy: "agent", CreatedClient: "claude"}
	if o.replaces != "" {
		p.Replaces = &o.replaces
	}
	if o.traced {
		p.TraceID, p.SpanID = testTraceID, testSpanID
	}
	if err := ts.db.InsertProposal(context.Background(), p, store.AuditEvent{At: now, Lane: "private", Actor: "agent",
		Client: "claude", Event: "proposal_pending", Target: p.ID, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	return p
}

// intervals is a workout using most step forms, with agent text that must be
// escaped.
func intervals(date civil.Date) workout.Workout {
	return workout.Workout{Name: "<b>Intervals</b>", Sport: "running", Date: date.String(), Notes: "Bring <water> & gels",
		Steps: []workout.Step{
			{StepBase: workout.StepBase{Kind: "warmup", Duration: &workout.Duration{Type: "time", TimeS: ip(600)},
				Target: &workout.Target{Type: "hr", LowBpm: ip(120), HighBpm: ip(140)}}},
			{StepBase: workout.StepBase{Kind: "repeat"}, Count: 6, Steps: []workout.Step2{
				{StepBase: workout.StepBase{Kind: "active", Duration: &workout.Duration{Type: "distance", DistanceM: ip(400)},
					Target: &workout.Target{Type: "pace", LowPaceSPerKm: ip(230), HighPaceSPerKm: ip(240)}}},
				{StepBase: workout.StepBase{Kind: "recovery", Duration: &workout.Duration{Type: "open"}}},
			}},
			{StepBase: workout.StepBase{Kind: "active", Duration: &workout.Duration{Type: "time", TimeS: ip(300)},
				Target: &workout.Target{Type: "effort", Cue: "<i>steady</i>"}}},
		}}
}

func (ts *testServer) status(t *testing.T, id string) store.ProposalStatus {
	t.Helper()
	p, err := ts.db.GetProposal(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

func (ts *testServer) page(t *testing.T, id string, cookie *http.Cookie) string {
	t.Helper()
	rec := ts.do(t, "GET", "/p/"+id, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /p/%s: status %d, body %s", id, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

var (
	ceremonyAttr = regexp.MustCompile(`data-ceremony="([^"]*)"`)
	optionsAttr  = regexp.MustCompile(`data-options="([^"]*)"`)
	urlAttr      = regexp.MustCompile(`data-url="([^"]*)"`)
)

// approveBody opens the proposal's page as the browser would, reads the
// approve button's ceremony and options, runs the authenticator and returns
// the body app.js posts to the button's data-url.
func (ts *testServer) approveBody(t *testing.T, id string) []byte {
	t.Helper()
	body := ts.page(t, id, nil)
	c, o, u := ceremonyAttr.FindStringSubmatch(body), optionsAttr.FindStringSubmatch(body), urlAttr.FindStringSubmatch(body)
	if c == nil || o == nil || u == nil || !strings.Contains(body, `data-action="approve"`) {
		t.Fatalf("no approve button on /p/%s: %s", id, body)
	}
	if got := html.UnescapeString(u[1]); got != "/p/"+id+"/approve" {
		t.Fatalf("data-url = %q", got)
	}
	cred, err := ts.authn.Get([]byte(html.UnescapeString(o[1])))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"ceremony": html.UnescapeString(c[1]), "credential": json.RawMessage(cred)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (ts *testServer) collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := ts.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	return rm
}

// deliveries returns coachd.proposals.deliveries by "<outcome>/<format>".
func (ts *testServer) deliveries(t *testing.T) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, sm := range ts.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "coachd.proposals.deliveries" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				if dp.Attributes.Len() != 2 {
					t.Errorf("deliveries attributes = %v, want outcome and format", dp.Attributes.ToSlice())
				}
				o, _ := dp.Attributes.Value(attribute.Key("outcome"))
				f, _ := dp.Attributes.Value(attribute.Key("format"))
				out[o.AsString()+"/"+f.AsString()] += dp.Value
			}
		}
	}
	return out
}

// approvalLatency returns the count and sum of coachd.proposals.approval_latency.
func (ts *testServer) approvalLatency(t *testing.T) (uint64, float64) {
	t.Helper()
	for _, sm := range ts.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "coachd.proposals.approval_latency" {
				continue
			}
			var n uint64
			var sum float64
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				if dp.Attributes.Len() != 0 {
					t.Errorf("approval latency attributes = %v, want none", dp.Attributes.ToSlice())
				}
				n += dp.Count
				sum += dp.Sum
			}
			return n, sum
		}
	}
	return 0, 0
}

func (ts *testServer) spansNamed(name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range ts.spans.Ended() {
		if s.Name() == name {
			out = append(out, s)
		}
	}
	return out
}

// checkProposalSpan checks that s carries proposal_id and links to the
// trace that proposed the workout.
func checkProposalSpan(t *testing.T, s sdktrace.ReadOnlySpan, id string) {
	t.Helper()
	var got string
	for _, kv := range s.Attributes() {
		if kv.Key == "proposal_id" {
			got = kv.Value.AsString()
		}
	}
	if got != id {
		t.Errorf("span %s: proposal_id = %q, want %q", s.Name(), got, id)
	}
	links := s.Links()
	if len(links) != 1 || links[0].SpanContext.TraceID().String() != testTraceID || links[0].SpanContext.SpanID().String() != testSpanID {
		t.Errorf("span %s: links = %+v, want the stored trace context", s.Name(), links)
	}
}

func TestProposalPageShowsContent(t *testing.T) {
	ts := newTestServer(t)
	date := civil.Date{Year: 2026, Month: time.October, Day: 28}
	p := ts.propose(t, intervals(date), proposeOpts{rationale: `<script>alert(1)</script> & threshold`,
		warnings: `[{"code":"spike_distance","message":"longest run in 30 days 8.0 km; planned 12.0 km"},` +
			`{"code":"same_day","message":"1 other workout planned that day"}]`})
	other := ts.addProposal(t, "<i>Strides</i>", date, store.ProposalApproved, "")
	declined := ts.addProposal(t, "declined", date, store.ProposalDeclined, "")
	nextDay := ts.addProposal(t, "next day", date.AddDays(1), store.ProposalApproved, "")

	rec := ts.do(t, "GET", "/p/"+p.ID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	checkPageHeaders(t, "GET /p/{id}", rec.Header(), "no-store")
	body := rec.Body.String()
	for _, want := range []string{
		agentSpan("&lt;b&gt;Intervals&lt;/b&gt;"),
		"Wed 28 Oct 2026", "running", "<strong>pending</strong>",
		// The steps are agent-written: one labelled block.
		`<div class="agent agent-block"><span class="agent-label">written by the agent</span>`,
		"Warm-up · 10:00 · HR 120–140 bpm", "Repeat 6×", "Active · 400 m · pace 3:50–4:00 /km",
		"Recovery · open (lap button)", "Active · 5:00 · effort: &lt;i&gt;steady&lt;/i&gt;",
		agentSpan("Bring &lt;water&gt; &amp; gels"),
		"What the agent said it is for", agentSpan("&lt;script&gt;alert(1)&lt;/script&gt; &amp; threshold"),
		"longest run in 30 days 8.0 km; planned 12.0 km", "1 other workout planned that day",
		"Also planned that day", `href="/p/` + other.ID + `"`, agentSpan("&lt;i&gt;Strides&lt;/i&gt;"),
		// No passkey yet: the enrol hint instead of the button. No session:
		// no decline form.
		"No passkey enrolled", "Sign in to decline",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{"<script>alert", "<b>Intervals", "<i>steady", "<water>", "<i>Strides",
		declined.ID, nextDay.ID, "data-ceremony", `action="/p/` + p.ID + `/decline"`} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains %q", bad)
		}
	}

	cookie := ts.enroll(t)
	body = ts.page(t, p.ID, nil)
	if !strings.Contains(body, `data-action="approve"`) || !ceremonyAttr.MatchString(body) || strings.Contains(body, "No passkey enrolled") {
		t.Errorf("page with a passkey lacks the approve button: %s", body)
	}
	body = ts.page(t, p.ID, cookie)
	if !strings.Contains(body, `action="/p/`+p.ID+`/decline"`) || strings.Contains(body, "Sign in to decline") {
		t.Errorf("page with a session lacks the decline form: %s", body)
	}
	// Only approved or delivered proposals count as planned: the approved
	// one's page does not list the pending one.
	if body := ts.page(t, other.ID, nil); strings.Contains(body, "Also planned that day") || strings.Contains(body, `href="/p/`+p.ID+`"`) {
		t.Errorf("an approved proposal lists a pending one as planned: %s", body)
	}
}

func TestApproveFlow(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	p := ts.propose(t, intervals(ts.today().AddDays(1)), proposeOpts{traced: true})
	ts.clock.Add(90 * time.Second)
	body := ts.approveBody(t, p.ID)
	rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", body, nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true,"replaced_withdrawn":false}` {
		t.Fatalf("approve: status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, p.ID); got != store.ProposalApproved {
		t.Fatalf("status = %s", got)
	}
	rows := ts.audit(t, "proposal_approved")
	want := store.AuditEvent{Lane: "public", Actor: "runner", Client: "passkey:1", Event: "proposal_approved", Target: p.ID, Outcome: "ok"}
	if len(rows) != 1 {
		t.Fatalf("proposal_approved rows = %+v", rows)
	}
	if got := rows[0]; !got.At.Equal(ts.clock.Now()) {
		t.Errorf("approved at %v, want %v", got.At, ts.clock.Now())
	}
	rows[0].At = time.Time{}
	if rows[0] != want {
		t.Fatalf("proposal_approved = %+v, want %+v", rows[0], want)
	}
	// A successful approval starts a session.
	cookie := sessionCookie(t, rec)
	page := ts.page(t, p.ID, cookie)
	if !signedIn(t, ts.do(t, "GET", "/p/"+p.ID, nil, cookie)) {
		t.Fatal("approval did not start a session")
	}
	for _, want := range []string{"<strong>approved</strong>", `action="/p/` + p.ID + `/fit"`, `action="/p/` + p.ID + `/withdraw"`} {
		if !strings.Contains(page, want) {
			t.Errorf("approved page lacks %q", want)
		}
	}
	if strings.Contains(page, `data-action="approve"`) {
		t.Error("approved page still offers approval")
	}
	pks, err := ts.db.ActivePasskeys(context.Background())
	if err != nil || len(pks) != 1 || pks[0].LastUsedAt == nil || !pks[0].LastUsedAt.Equal(ts.clock.Now()) {
		t.Fatalf("approval did not record the passkey's use: %+v, %v", pks, err)
	}
	if n, sum := ts.approvalLatency(t); n != 1 || sum != 90 {
		t.Fatalf("approval latency: count %d, sum %v; want 1, 90", n, sum)
	}
	spans := ts.spansNamed("proposal.approve")
	if len(spans) != 1 {
		t.Fatalf("proposal.approve spans = %d", len(spans))
	}
	checkProposalSpan(t, spans[0], p.ID)
}

func TestApproveReplayRejected(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	p := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
	body := ts.approveBody(t, p.ID)
	if rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", body, nil); rec.Code != http.StatusOK {
		t.Fatalf("first: status %d, body %s", rec.Code, rec.Body)
	}
	rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", body, nil)
	if rec.Code != http.StatusConflict || jsonError(t, rec) != "already used; reload the page" {
		t.Fatalf("replay: status %d, body %s", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("replay set a cookie")
	}
	if n := len(ts.audit(t, "proposal_approved")); n != 1 {
		t.Fatalf("proposal_approved rows = %d, want 1", n)
	}
	// The failure row names the route, not the client-chosen path.
	if f := ts.audit(t, "passkey_failure"); len(f) != 1 || f[0].Outcome != "used" || f[0].Target != "/p/{id}/approve" {
		t.Fatalf("passkey_failure rows = %+v", f)
	}
}

// A double tap: concurrent submits of one assertion approve exactly once.
func TestApproveConcurrentDoubleSubmit(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	p := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
	body := ts.approveBody(t, p.ID)
	const n = 4
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			req := httptest.NewRequest("POST", testOrigin+"/p/"+p.ID+"/approve", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			codes[i] = ts.serve(req).Code
		})
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("status codes %v: want exactly one 200", codes)
	}
	if rows := ts.audit(t, "proposal_approved"); len(rows) != 1 {
		t.Fatalf("proposal_approved rows = %d, want 1", len(rows))
	}
}

func TestApproveExpires(t *testing.T) {
	tz, err := time.LoadLocation("Europe/Bratislava")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		now  time.Time
		date civil.Date
		want time.Time
	}{
		"start of the next day": {time.Date(2026, 10, 24, 23, 50, 0, 0, tz), civil.Date{Year: 2026, Month: 10, Day: 24},
			time.Date(2026, 10, 25, 0, 0, 0, 0, tz)},
		// 25 Oct 2026 has 25 hours in Bratislava: the day ends at 23:00 UTC.
		"DST end day": {time.Date(2026, 10, 25, 10, 0, 0, 0, tz), civil.Date{Year: 2026, Month: 10, Day: 25},
			time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC)},
		// 29 Mar 2026 has 23 hours: it ends at 22:00 UTC.
		"DST start day": {time.Date(2026, 3, 29, 10, 0, 0, 0, tz), civil.Date{Year: 2026, Month: 3, Day: 29},
			time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC)},
		"24 hours at most": {time.Date(2026, 10, 23, 11, 0, 0, 0, tz), civil.Date{Year: 2026, Month: 10, Day: 30},
			time.Date(2026, 10, 24, 11, 0, 0, 0, tz)},
	} {
		if got := approveExpires(tc.now, tc.date, tz); !got.Equal(tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// Review focus 1: the page was left open overnight.
func TestApproveAfterMidnightRefused(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	tz := ts.srv.d.TZ
	date := civil.Date{Year: 2026, Month: time.October, Day: 24}
	p := ts.addProposal(t, "Long run", date, store.ProposalPending, "")
	ts.clock.Set(time.Date(2026, 10, 24, 23, 50, 0, 0, tz))
	body := ts.approveBody(t, p.ID)

	ts.clock.Set(time.Date(2026, 10, 25, 0, 10, 0, 0, tz))
	rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", body, nil)
	if rec.Code != http.StatusConflict || jsonError(t, rec) != "this approval expired; reload the page" {
		t.Fatalf("approve after midnight: status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, p.ID); got != store.ProposalPending {
		t.Fatalf("status = %s", got)
	}
	if n := len(ts.audit(t, "proposal_approved")); n != 0 {
		t.Fatalf("proposal_approved rows = %d", n)
	}
	if f := ts.audit(t, "passkey_failure"); len(f) != 1 || f[0].Outcome != "expired" || f[0].Target != "/p/{id}/approve" {
		t.Fatalf("passkey_failure rows = %+v", f)
	}

	page := ts.page(t, p.ID, nil)
	if !strings.Contains(page, "this workout's date has passed") {
		t.Errorf("reloaded page lacks the date-passed notice: %s", page)
	}
	if strings.Contains(page, "data-ceremony") || strings.Contains(page, `data-action="approve"`) {
		t.Error("reloaded page still offers approval")
	}
}

func TestApproveCeremonyForOtherProposal(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	a := ts.addProposal(t, "A", ts.today().AddDays(1), store.ProposalPending, "")
	b := ts.addProposal(t, "B", ts.today().AddDays(2), store.ProposalPending, "")
	body := ts.approveBody(t, a.ID)
	rec := ts.do(t, "POST", "/p/"+b.ID+"/approve", body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if ts.status(t, a.ID) != store.ProposalPending || ts.status(t, b.ID) != store.ProposalPending {
		t.Fatal("a proposal changed")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("refused approval set a cookie")
	}
	// The ceremony was not used up: it still approves its own proposal.
	if rec := ts.do(t, "POST", "/p/"+a.ID+"/approve", body, nil); rec.Code != http.StatusOK {
		t.Fatalf("own proposal: status %d, body %s", rec.Code, rec.Body)
	}
}

// Review focus 4: approving a replacement whose original was delivered.
func TestApproveReplacementShowsWatchHint(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	date := ts.today().AddDays(2)
	old := ts.addProposal(t, "Tempo", date, store.ProposalDelivered, "")
	short := intervals(date)
	short.Name = "Tempo, shorter"
	repl := ts.propose(t, short, proposeOpts{replaces: old.ID})

	page := ts.page(t, repl.ID, nil)
	for _, want := range []string{"Replaces", `href="/p/` + old.ID + `"`, agentSpan("Tempo"), "delivered", "Warm-up · 10:00"} {
		if !strings.Contains(page, want) {
			t.Errorf("replacement page lacks %q", want)
		}
	}

	body := ts.approveBody(t, repl.ID)
	rec := ts.do(t, "POST", "/p/"+repl.ID+"/approve", body, cookie)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true,"replaced_withdrawn":true}` {
		t.Fatalf("approve: status %d, body %s", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("approval inside a session started another session")
	}
	if ts.status(t, old.ID) != store.ProposalWithdrawn || ts.status(t, repl.ID) != store.ProposalApproved {
		t.Fatalf("statuses: old %s, replacement %s", ts.status(t, old.ID), ts.status(t, repl.ID))
	}
	if w := ts.audit(t, "proposal_withdrawn"); len(w) != 1 || w[0].Target != old.ID || w[0].Client != "passkey:1" {
		t.Fatalf("proposal_withdrawn rows = %+v", w)
	}
	oldPage := ts.page(t, old.ID, nil)
	for _, want := range []string{watchHint, "<strong>withdrawn</strong>", "Replaced by", `href="/p/` + repl.ID + `"`} {
		if !strings.Contains(oldPage, want) {
			t.Errorf("replaced proposal's page lacks %q", want)
		}
	}
	// The page app.js reloads after approving shows the hint as well.
	if !strings.Contains(ts.page(t, repl.ID, cookie), watchHint) {
		t.Error("replacement page lacks the watch hint")
	}
}

// Review focus 3: a client holding only the Access JWT.
func TestRunnerWritesNeedSession(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	pend := ts.addProposal(t, "pending", ts.today().AddDays(1), store.ProposalPending, "")
	appr := ts.addProposal(t, "approved", ts.today().AddDays(1), store.ProposalApproved, "")
	for _, cookie := range []*http.Cookie{nil, {Name: "__Host-coachd_session", Value: strings.Repeat("A", 43)}} {
		for path, p := range map[string]store.Proposal{
			"/p/" + pend.ID + "/decline":  pend,
			"/p/" + appr.ID + "/withdraw": appr,
			"/p/" + appr.ID + "/fit":      appr,
			"/p/" + appr.ID + "/zwo":      appr,
		} {
			rec := ts.do(t, "POST", path, url.Values{}, cookie)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sign in with your passkey first") {
				t.Errorf("POST %s: status %d", path, rec.Code)
			}
			if got := ts.status(t, p.ID); got != p.Status {
				t.Errorf("POST %s changed the status to %s", path, got)
			}
		}
	}
	for _, ev := range []string{"proposal_declined", "proposal_withdrawn", "proposal_delivered"} {
		if rows := ts.audit(t, ev); len(rows) != 0 {
			t.Errorf("%s rows = %+v", ev, rows)
		}
	}
	if d := ts.deliveries(t); len(d) != 0 {
		t.Errorf("deliveries = %v", d)
	}
}

// The owner's usual case: approving the workout planned for today, from
// morning until the day's last minutes.
func TestApproveToday(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	for _, at := range []time.Time{t0, time.Date(2026, 10, 23, 23, 55, 0, 0, ts.srv.d.TZ)} {
		ts.clock.Set(at)
		p := ts.addProposal(t, "Easy", ts.today(), store.ProposalPending, "")
		page := ts.page(t, p.ID, nil)
		if strings.Contains(page, "this workout's date has passed") {
			t.Fatalf("%v: today's workout shown as past: %s", at, page)
		}
		rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", ts.approveBody(t, p.ID), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%v: approve: status %d, body %s", at, rec.Code, rec.Body)
		}
		if got := ts.status(t, p.ID); got != store.ProposalApproved {
			t.Fatalf("%v: status = %s", at, got)
		}
	}
	if n := len(ts.audit(t, "proposal_approved")); n != 2 {
		t.Fatalf("proposal_approved rows = %d, want 2", n)
	}
}

// stranger returns an authenticator holding a credential coachd never
// enrolled.
func stranger(t *testing.T) *passkeytest.Authenticator {
	t.Helper()
	a := passkeytest.New(testOrigin)
	if _, err := a.Create([]byte(`{"publicKey":{"challenge":"c3RyYW5nZXI","user":{"id":"Y29hY2hkLXJ1bm5lcg"}}}`)); err != nil {
		t.Fatal(err)
	}
	return a
}

// withoutAllowList drops allowCredentials from request options, as a client
// offering a credential the server did not list would.
func withoutAllowList(t *testing.T, options []byte) []byte {
	t.Helper()
	var o map[string]map[string]any
	if err := json.Unmarshal(options, &o); err != nil {
		t.Fatal(err)
	}
	delete(o["publicKey"], "allowCredentials")
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// R59: approval needs a fresh assertion bound to the proposal even inside a
// session. A valid session cookie with a missing, foreign, unverified or
// wrong-purpose assertion approves nothing.
func TestApproveWithSessionNeedsAssertion(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
	page := ts.page(t, p.ID, cookie)
	c, o := ceremonyAttr.FindStringSubmatch(page), optionsAttr.FindStringSubmatch(page)
	if c == nil || o == nil {
		t.Fatalf("no approve button: %s", page)
	}
	ceremony, options := html.UnescapeString(c[1]), []byte(html.UnescapeString(o[1]))
	sign := func(a *passkeytest.Authenticator, opts []byte) json.RawMessage {
		t.Helper()
		cred, err := a.Get(opts)
		if err != nil {
			t.Fatal(err)
		}
		return cred
	}
	cases := []struct {
		name    string
		body    func() any
		status  int
		msg     string
		outcome string // the passkey_failure row; "" for none
	}{
		{"missing credential", func() any { return map[string]any{"ceremony": ceremony} }, 400, msgMalformed, ""},
		{"null credential", func() any { return map[string]any{"ceremony": ceremony, "credential": nil} }, 400, msgMalformed, ""},
		{"missing ceremony", func() any { return map[string]any{"credential": sign(ts.authn, options)} }, 400, msgMalformed, ""},
		{"credential from another authenticator", func() any {
			return map[string]any{"ceremony": ceremony, "credential": sign(stranger(t), withoutAllowList(t, options))}
		}, 401, msgCheckFailed, "unknown_credential"},
		{"user not verified", func() any {
			ts.authn.SetUserVerified(false)
			defer ts.authn.SetUserVerified(true)
			return map[string]any{"ceremony": ceremony, "credential": sign(ts.authn, options)}
		}, 401, msgCheckFailed, "invalid"},
		{"login ceremony", func() any { return json.RawMessage(ts.loginFinishBody(t)) }, 409, msgApproveExpired, "invalid"},
		{"forged ceremony", func() any {
			return map[string]any{"ceremony": "eyJwIjoiYXBwcm92ZSJ9.AAAA", "credential": sign(ts.authn, options)}
		}, 409, msgApproveExpired, "invalid"},
	}
	failures := 0
	for _, tc := range cases {
		rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", tc.body(), cookie)
		if rec.Code != tc.status || jsonError(t, rec) != tc.msg {
			t.Errorf("%s: status %d, body %s", tc.name, rec.Code, rec.Body)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: set a cookie", tc.name)
		}
		if got := ts.status(t, p.ID); got != store.ProposalPending {
			t.Fatalf("%s: status = %s", tc.name, got)
		}
		if n := len(ts.audit(t, "proposal_approved")); n != 0 {
			t.Fatalf("%s: proposal_approved rows = %d", tc.name, n)
		}
		f := ts.audit(t, "passkey_failure")
		if tc.outcome != "" {
			failures++
			if len(f) != failures || f[0].Outcome != tc.outcome || f[0].Target != "/p/{id}/approve" {
				t.Errorf("%s: passkey_failure rows = %+v", tc.name, f)
			}
		} else if len(f) != failures {
			t.Errorf("%s: passkey_failure rows = %+v", tc.name, f)
		}
	}
	// None of them used up the page's ceremony: the runner's own assertion
	// still approves.
	rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", map[string]any{"ceremony": ceremony, "credential": sign(ts.authn, options)}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("own assertion: status %d, body %s", rec.Code, rec.Body)
	}
	if n := len(ts.audit(t, "proposal_approved")); n != 1 {
		t.Fatalf("proposal_approved rows = %d", n)
	}
}

// A synced passkey (iCloud Keychain, Google Password Manager) reports the
// backup flags and a sign count that stays 0. It enrols, signs in and
// approves; a replayed approval is "already used", not a clone warning.
func TestSyncedPasskey(t *testing.T) {
	ts := newTestServer(t)
	ts.authn.SetSynced(true)
	ts.enroll(t)
	pks, err := ts.db.ActivePasskeys(context.Background())
	if err != nil || len(pks) != 1 {
		t.Fatalf("passkeys = %+v, %v", pks, err)
	}
	var stored struct {
		Flags struct {
			BackupEligible bool `json:"backupEligible"`
			BackupState    bool `json:"backupState"`
		} `json:"flags"`
		Authenticator struct {
			SignCount uint32 `json:"signCount"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal(pks[0].CredentialJSON, &stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Flags.BackupEligible || !stored.Flags.BackupState || stored.Authenticator.SignCount != 0 {
		t.Fatalf("stored credential is not a synced one: %s", pks[0].CredentialJSON)
	}
	ts.login(t)
	ts.login(t)

	p := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
	body := ts.approveBody(t, p.ID)
	if rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", body, nil); rec.Code != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", rec.Code, rec.Body)
	}
	rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", body, nil)
	if rec.Code != http.StatusConflict || jsonError(t, rec) != msgApproveUsed {
		t.Fatalf("replay: status %d, body %s", rec.Code, rec.Body)
	}
	if f := ts.audit(t, "passkey_failure"); len(f) != 1 || f[0].Outcome != "used" {
		t.Fatalf("passkey_failure rows = %+v", f)
	}
	// Another approval with the counter still at 0 is no clone either.
	q := ts.addProposal(t, "Long", ts.today().AddDays(2), store.ProposalPending, "")
	if rec := ts.do(t, "POST", "/p/"+q.ID+"/approve", ts.approveBody(t, q.ID), nil); rec.Code != http.StatusOK {
		t.Fatalf("second approval: status %d, body %s", rec.Code, rec.Body)
	}
	if n := len(ts.audit(t, "proposal_approved")); n != 2 {
		t.Fatalf("proposal_approved rows = %d", n)
	}
	if n := len(ts.audit(t, "passkey_login")); n != 2 {
		t.Fatalf("passkey_login rows = %d", n)
	}
}

// Once the approval is committed the answer is ok, even when recording the
// passkey's use or starting the session then fails: that is logged, and no
// session is started.
func TestApproveCommittedDespiteSessionFailure(t *testing.T) {
	for name, trigger := range map[string]string{
		"passkey use": `CREATE TRIGGER fail_after_approve BEFORE UPDATE ON passkeys BEGIN SELECT RAISE(ABORT, 'injected'); END`,
		"session":     `CREATE TRIGGER fail_after_approve BEFORE INSERT ON web_sessions BEGIN SELECT RAISE(ABORT, 'injected'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t)
			ts.enroll(t)
			p := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
			body := ts.approveBody(t, p.ID)
			ts.rawExec(t, trigger)
			rec := ts.do(t, "POST", "/p/"+p.ID+"/approve", body, nil)
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true,"replaced_withdrawn":false}` {
				t.Fatalf("approve: status %d, body %s", rec.Code, rec.Body)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatalf("a session was started: %q", rec.Header().Values("Set-Cookie"))
			}
			if got := ts.status(t, p.ID); got != store.ProposalApproved {
				t.Fatalf("status = %s", got)
			}
			if n := len(ts.audit(t, "proposal_approved")); n != 1 {
				t.Fatalf("proposal_approved rows = %d", n)
			}
			logs := ts.logs.String()
			if !strings.Contains(logs, `"msg":"approval committed; no session started"`) || !strings.Contains(logs, `"error_class":`) ||
				!strings.Contains(logs, `"proposal_id":"`+p.ID+`"`) {
				t.Fatalf("logs: %s", logs)
			}
			if spans := ts.spansNamed("proposal.approve"); len(spans) != 1 {
				t.Fatalf("proposal.approve spans = %d", len(spans))
			}
		})
	}
}

func TestDecline(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
	rec := ts.do(t, "POST", "/p/"+p.ID+"/decline", url.Values{}, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/p/"+p.ID {
		t.Fatalf("decline: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := ts.status(t, p.ID); got != store.ProposalDeclined {
		t.Fatalf("status = %s", got)
	}
	rows := ts.audit(t, "proposal_declined")
	want := store.AuditEvent{Lane: "public", Actor: "runner", Client: "passkey:1", Event: "proposal_declined", Target: p.ID, Outcome: "ok"}
	if len(rows) != 1 {
		t.Fatalf("proposal_declined rows = %+v", rows)
	}
	rows[0].At = time.Time{}
	if rows[0] != want {
		t.Fatalf("proposal_declined = %+v, want %+v", rows[0], want)
	}
	page := ts.page(t, p.ID, cookie)
	if !strings.Contains(page, "<strong>declined</strong>") || strings.Contains(page, `data-action="approve"`) ||
		strings.Contains(page, `/decline"`) {
		t.Errorf("declined page: %s", page)
	}
	// Declining again is a conflict, shown on the page.
	rec = ts.do(t, "POST", "/p/"+p.ID+"/decline", url.Values{}, cookie)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "this proposal is declined") {
		t.Fatalf("second decline: status %d, body %s", rec.Code, rec.Body)
	}
	if n := len(ts.audit(t, "proposal_declined")); n != 1 {
		t.Fatalf("proposal_declined rows = %d", n)
	}
}

func TestWithdrawDelivered(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.addProposal(t, "Tempo", ts.today().AddDays(1), store.ProposalDelivered, "")
	page := ts.page(t, p.ID, cookie)
	for _, want := range []string{`action="/p/` + p.ID + `/fit"`, `action="/p/` + p.ID + `/withdraw"`} {
		if !strings.Contains(page, want) {
			t.Errorf("delivered page lacks %q", want)
		}
	}
	if !strings.Contains(ts.page(t, p.ID, nil), "Sign in to download or withdraw") {
		t.Error("signed-out delivered page lacks the sign-in hint")
	}
	rec := ts.do(t, "POST", "/p/"+p.ID+"/withdraw", url.Values{}, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/p/"+p.ID {
		t.Fatalf("withdraw: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := ts.status(t, p.ID); got != store.ProposalWithdrawn {
		t.Fatalf("status = %s", got)
	}
	if w := ts.audit(t, "proposal_withdrawn"); len(w) != 1 || w[0].Target != p.ID || w[0].Client != "passkey:1" || w[0].Lane != "public" {
		t.Fatalf("proposal_withdrawn rows = %+v", w)
	}
	page = ts.page(t, p.ID, cookie)
	if !strings.Contains(page, watchHint) || strings.Contains(page, `/fit"`) {
		t.Errorf("withdrawn page: %s", page)
	}
	// A pending proposal cannot be withdrawn.
	pend := ts.addProposal(t, "pending", ts.today().AddDays(1), store.ProposalPending, "")
	rec = ts.do(t, "POST", "/p/"+pend.ID+"/withdraw", url.Values{}, cookie)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "this proposal is pending") {
		t.Fatalf("withdraw pending: status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, pend.ID); got != store.ProposalPending {
		t.Fatalf("pending status = %s", got)
	}
}

func decodeFIT(t *testing.T, b []byte) (*mesgdef.FileId, *mesgdef.Workout, int) {
	t.Helper()
	fit, err := decoder.New(bytes.NewReader(b)).Decode()
	if err != nil {
		t.Fatalf("decode FIT: %v", err)
	}
	var fid *mesgdef.FileId
	var wk *mesgdef.Workout
	steps := 0
	for i := range fit.Messages {
		m := fit.Messages[i]
		switch m.Num {
		case mesgnum.FileId:
			fid = mesgdef.NewFileId(&m)
		case mesgnum.Workout:
			wk = mesgdef.NewWorkout(&m)
		case mesgnum.WorkoutStep:
			steps++
		}
	}
	if fid == nil || wk == nil {
		t.Fatal("FIT lacks file_id or workout")
	}
	return fid, wk, steps
}

func TestFITDownload(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.propose(t, intervals(ts.today().AddDays(1)), proposeOpts{status: store.ProposalApproved, traced: true})
	ts.clock.Add(time.Hour) // the file's creation time is the proposal's, not the download's

	rec := ts.do(t, "POST", "/p/"+p.ID+"/fit", url.Values{}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: status %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.ant.fit" {
		t.Errorf("Content-Type = %q", ct)
	}
	wantCD := `attachment; filename="coachd-2026-10-24-` + p.ID[:8] + `.fit"`
	if cd := rec.Header().Get("Content-Disposition"); cd != wantCD {
		t.Errorf("Content-Disposition = %q, want %q", cd, wantCD)
	}
	checkPageHeaders(t, "POST /p/{id}/fit", rec.Header(), "no-store")
	first := rec.Body.Bytes()
	fid, wk, steps := decodeFIT(t, first)
	if !fid.TimeCreated.Equal(p.CreatedAt) {
		t.Errorf("TimeCreated = %v, want %v", fid.TimeCreated, p.CreatedAt)
	}
	if wk.WktName != p.Name || steps != 5 {
		t.Errorf("workout %q with %d steps", wk.WktName, steps)
	}
	if got := ts.status(t, p.ID); got != store.ProposalDelivered {
		t.Fatalf("status = %s", got)
	}
	rows := ts.audit(t, "proposal_delivered")
	if len(rows) != 1 || rows[0].Target != p.ID || rows[0].Client != "passkey:1" || rows[0].Lane != "public" || rows[0].Actor != "runner" {
		t.Fatalf("proposal_delivered rows = %+v", rows)
	}

	rec = ts.do(t, "POST", "/p/"+p.ID+"/fit", url.Values{}, cookie)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), first) {
		t.Fatalf("second download: status %d, same bytes %v", rec.Code, bytes.Equal(rec.Body.Bytes(), first))
	}
	if n := len(ts.audit(t, "proposal_delivered")); n != 1 {
		t.Fatalf("proposal_delivered rows = %d after a second download", n)
	}
	if d := ts.deliveries(t); d["delivered/fit"] != 1 || d["redownload/fit"] != 1 || len(d) != 2 {
		t.Fatalf("deliveries = %v", d)
	}
	spans := ts.spansNamed("proposal.deliver")
	if len(spans) != 2 {
		t.Fatalf("proposal.deliver spans = %d", len(spans))
	}
	if f := spanFormat(t, ts); len(f) != 2 || f[0] != "fit" || f[1] != "fit" {
		t.Fatalf("span formats = %v", f)
	}
	for _, s := range spans {
		checkProposalSpan(t, s, p.ID)
	}
}

func TestFITDownloadPendingConflict(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	for _, st := range []store.ProposalStatus{store.ProposalPending, store.ProposalDeclined, store.ProposalWithdrawn} {
		p := ts.addProposal(t, string(st), ts.today().AddDays(1), st, "")
		rec := ts.do(t, "POST", "/p/"+p.ID+"/fit", url.Values{}, cookie)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "this proposal is "+string(st)) {
			t.Errorf("%s: status %d, body %s", st, rec.Code, rec.Body)
		}
		if got := ts.status(t, p.ID); got != st {
			t.Errorf("%s: status changed to %s", st, got)
		}
	}
	if rows := ts.audit(t, "proposal_delivered"); len(rows) != 0 {
		t.Fatalf("proposal_delivered rows = %+v", rows)
	}
	if d := ts.deliveries(t); len(d) != 0 {
		t.Fatalf("deliveries = %v", d)
	}
}

// A stored workout the encoder refuses is a 500 with no transition.
func TestFITEncodeFailure(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	p := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalApproved, "")
	bad := strings.Replace(string(p.ContentJSON), `"sport":"running"`, `"sport":"curling"`, 1)
	ts.rawExec(t, `UPDATE proposals SET content_json=?, content_hash=? WHERE id=?`, bad, workout.ContentHash([]byte(bad)), p.ID)
	rec := ts.do(t, "POST", "/p/"+p.ID+"/fit", url.Values{}, cookie)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "could not encode this workout as FIT") {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, p.ID); got != store.ProposalApproved {
		t.Fatalf("status = %s", got)
	}
	if rows := ts.audit(t, "proposal_delivered"); len(rows) != 0 {
		t.Fatalf("proposal_delivered rows = %+v", rows)
	}
}

func TestProposalIntegrityMismatch(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	tamper := func(p store.Proposal) {
		t.Helper()
		changed := strings.Replace(string(p.ContentJSON), `"time_s":600`, `"time_s":6000`, 1)
		if changed == string(p.ContentJSON) {
			t.Fatal("nothing to tamper with")
		}
		ts.rawExec(t, `UPDATE proposals SET content_json=? WHERE id=?`, changed, p.ID)
	}
	pend := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
	tamper(pend)
	page := ts.page(t, pend.ID, cookie)
	if !strings.Contains(page, "stored content does not match its hash; approval disabled") {
		t.Errorf("page lacks the integrity notice: %s", page)
	}
	if strings.Contains(page, `data-action="approve"`) || strings.Contains(page, "data-ceremony") {
		t.Error("page offers approval of tampered content")
	}
	if !strings.Contains(page, "Warm-up · 1:40:00") {
		t.Error("page does not show what is stored")
	}

	// Content changed after the page was rendered: the ceremony binds the
	// hash of what the runner saw, which the stored bytes no longer match.
	late := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalPending, "")
	body := ts.approveBody(t, late.ID)
	tamper(late)
	rec := ts.do(t, "POST", "/p/"+late.ID+"/approve", body, nil)
	if rec.Code != http.StatusConflict || jsonError(t, rec) != "content changed; reload the page" {
		t.Fatalf("approve after tampering: status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, late.ID); got != store.ProposalPending {
		t.Fatalf("tampered proposal status = %s", got)
	}

	// An approved one is not downloaded either.
	appr := ts.addProposal(t, "Easy", ts.today().AddDays(1), store.ProposalApproved, "")
	tamper(appr)
	page = ts.page(t, appr.ID, cookie)
	if !strings.Contains(page, "stored content does not match its hash; download disabled") || strings.Contains(page, `/fit"`) {
		t.Errorf("approved page: %s", page)
	}
	rec = ts.do(t, "POST", "/p/"+appr.ID+"/fit", url.Values{}, cookie)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "stored content does not match its hash") {
		t.Fatalf("download: status %d, body %s", rec.Code, rec.Body)
	}
	if got := ts.status(t, appr.ID); got != store.ProposalApproved {
		t.Fatalf("status = %s", got)
	}
}

func TestUnknownProposal404(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.enroll(t)
	const id = "00000000000000000000000000000000"
	rec := ts.do(t, "GET", "/p/"+id, nil, nil)
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET: status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, action := range []string{"decline", "withdraw", "fit"} {
		if rec := ts.do(t, "POST", "/p/"+id+"/"+action, url.Values{}, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: status %d", action, rec.Code)
		}
	}
	rec = ts.do(t, "POST", "/p/"+id+"/approve", `{"ceremony":"x.y","credential":{"id":"x"}}`, nil)
	if rec.Code != http.StatusNotFound || jsonError(t, rec) != "proposal not found" {
		t.Fatalf("approve: status %d, body %s", rec.Code, rec.Body)
	}
	if f := ts.audit(t, "passkey_failure"); len(f) != 0 {
		t.Fatalf("passkey_failure rows = %+v", f)
	}
}
