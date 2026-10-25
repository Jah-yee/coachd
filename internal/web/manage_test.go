package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/passkey/passkeytest"
	"github.com/stefiix92/coachd/internal/store"
)

func (ts *testServer) addNote(t *testing.T, cat store.NoteCategory, text, by string) store.Note {
	t.Helper()
	n, err := ts.db.InsertNote(context.Background(), store.Note{Category: cat, Text: text, CreatedBy: by,
		CreatedAt: ts.clock.Now(), UpdatedAt: ts.clock.Now()}, store.MaxNotes)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func noteForm(cat, text string) url.Values { return url.Values{"category": {cat}, "text": {text}} }

func TestNotesCRUD(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)

	rec := ts.do(t, "POST", "/notes", noteForm("injury", "left knee, easy weeks"), c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/notes" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if got := ts.audit(t, "note_created"); len(got) != 1 || got[0].Actor != "runner" || got[0].Lane != "public" ||
		!strings.HasPrefix(got[0].Client, "passkey:") {
		t.Fatalf("note_created audit = %+v", got)
	}

	a := ts.addNote(t, store.NotePreference, "no mornings", "agent:private:claude")
	page := ts.do(t, "GET", "/notes", nil, c).Body.String()
	if !strings.Contains(page, "agent (claude)") || !strings.Contains(page, "you") || !strings.Contains(page, "written by the agent") {
		t.Fatalf("page lacks authors: %s", page)
	}

	id := strconv.FormatInt(a.ID, 10)
	ts.clock.Add(time.Minute)
	rec = ts.do(t, "POST", "/notes/"+id, noteForm("availability", "evenings only"), c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/notes" {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	ns, _ := ts.db.ListNotes(context.Background())
	var found bool
	for _, n := range ns {
		if n.ID == a.ID {
			found = true
			if n.CreatedBy != "runner" || n.Text != "evenings only" || n.Category != store.NoteAvailability {
				t.Fatalf("edited note = %+v", n)
			}
		}
	}
	if !found {
		t.Fatal("note vanished")
	}
	if page := ts.do(t, "GET", "/notes", nil, c).Body.String(); strings.Contains(page, "agent (claude)") {
		t.Fatalf("edited note still agent-labelled: %s", page)
	}

	rec = ts.do(t, "POST", "/notes/"+id+"/delete", url.Values{}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/notes" {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if len(ts.audit(t, "note_created")) != 1 || len(ts.audit(t, "note_updated")) != 1 || len(ts.audit(t, "note_deleted")) != 1 {
		t.Fatal("want one audit row each for create, update, delete")
	}
	if got := ts.audit(t, "note_deleted")[0]; got.Target != id {
		t.Fatalf("delete target = %q, want %s", got.Target, id)
	}

	for _, p := range []string{"/notes/999", "/notes/999/delete", "/notes/abc", "/notes/abc/delete"} {
		if rec := ts.do(t, "POST", p, noteForm("other", "x"), c); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", p, rec.Code)
		}
	}
	if len(ts.audit(t, "note_updated")) != 1 || len(ts.audit(t, "note_deleted")) != 1 {
		t.Fatal("missing note wrote audit rows")
	}
}

func TestNotesValidation(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	cases := []struct{ name, cat, text, want string }{
		{"category", "mood", "x", "category must be one of injury, availability, preference, other"},
		{"long", "other", strings.Repeat("a", store.MaxNoteRunes+1), "exceeds the 500 limit"},
		{"control", "other", "bad\x07bell", "control character"},
	}
	for _, tc := range cases {
		rec := ts.do(t, "POST", "/notes", noteForm(tc.cat, tc.text), c)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d, body lacks %q", tc.name, rec.Code, tc.want)
		}
	}
	for i := range store.MaxNotes {
		ts.addNote(t, store.NoteOther, "n"+strconv.Itoa(i), "runner")
	}
	rec := ts.do(t, "POST", "/notes", noteForm("other", "one more"), c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "notes are full (50 of 50); delete one first") {
		t.Fatalf("full: %d %s", rec.Code, rec.Body)
	}
	if n, _ := ts.db.ListNotes(context.Background()); len(n) != store.MaxNotes {
		t.Fatalf("notes = %d", len(n))
	}
	if len(ts.audit(t, "note_created")) != 0 {
		t.Fatal("failed creates were audited")
	}
	// An invalid edit changes nothing.
	n, _ := ts.db.ListNotes(context.Background())
	rec = ts.do(t, "POST", "/notes/"+strconv.FormatInt(n[0].ID, 10), noteForm("other", "bad\x07"), c)
	if rec.Code != http.StatusBadRequest || len(ts.audit(t, "note_updated")) != 0 {
		t.Fatalf("edit: %d", rec.Code)
	}
}

func TestNotesAndPasskeysWritesNeedSession(t *testing.T) {
	ts := newTestServer(t)
	ts.enroll(t)
	n := ts.addNote(t, store.NoteOther, "keep", "agent:private:claude")
	id := strconv.FormatInt(n.ID, 10)
	for _, p := range []string{"/notes", "/notes/" + id, "/notes/" + id + "/delete", "/passkeys/1/revoke"} {
		if rec := ts.do(t, "POST", p, noteForm("injury", "x"), nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s: %d, want 401", p, rec.Code)
		}
	}
	ns, _ := ts.db.ListNotes(context.Background())
	if len(ns) != 1 || ns[0].Text != "keep" || ns[0].CreatedBy != "agent:private:claude" {
		t.Fatalf("notes changed: %+v", ns)
	}
	if pks, _ := ts.db.ActivePasskeys(context.Background()); len(pks) != 1 {
		t.Fatal("passkey revoked without a session")
	}
	for _, e := range []string{"note_created", "note_updated", "note_deleted", "passkey_revoked"} {
		if len(ts.audit(t, e)) != 0 {
			t.Errorf("%s audited", e)
		}
	}
}

func TestRevokeCurrentPasskeySignsOut(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	pks, _ := ts.db.ActivePasskeys(context.Background())
	id := strconv.FormatInt(pks[0].ID, 10)
	page := ts.do(t, "GET", "/passkeys", nil, c).Body.String()
	if !strings.Contains(page, "phone") {
		t.Fatalf("passkeys page lacks label: %s", page)
	}
	rec := ts.do(t, "POST", "/passkeys/"+id+"/revoke", url.Values{}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if cc := sessionCookie(t, rec); cc.MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", cc)
	}
	if rec := ts.do(t, "POST", "/notes", noteForm("other", "x"), c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old session still works: %d", rec.Code)
	}
	if got := ts.audit(t, "passkey_revoked"); len(got) != 1 || got[0].Target != id {
		t.Fatalf("audit = %+v", got)
	}
	// Already revoked: 404.
	if rec := ts.do(t, "POST", "/passkeys/"+id+"/revoke", url.Values{}, c); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusNotFound {
		t.Fatalf("second revoke: %d", rec.Code)
	}
}

func TestRevokeOtherPasskey(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	ts.authn = passkeytest.New(testOrigin) // a second device
	if rec := ts.enrollWith(t, ts.newEnrollment(t), "tablet"); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	pks, _ := ts.db.ActivePasskeys(context.Background())
	if len(pks) != 2 {
		t.Fatalf("passkeys = %d", len(pks))
	}
	ws, _ := ts.db.SessionByHash(context.Background(), hashToken(c.Value), ts.clock.Now())
	var other int64
	for _, p := range pks {
		if p.ID != ws.PasskeyID {
			other = p.ID
		}
	}
	oid := strconv.FormatInt(other, 10)
	rec := ts.do(t, "POST", "/passkeys/"+oid+"/revoke", url.Values{}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/passkeys" {
		t.Fatalf("revoke other: %d", rec.Code)
	}
	if rec := ts.do(t, "POST", "/notes", noteForm("other", "x"), c); rec.Code != http.StatusSeeOther {
		t.Fatalf("session lost: %d", rec.Code)
	}
	for _, p := range []string{"/passkeys/" + oid + "/revoke", "/passkeys/999/revoke", "/passkeys/abc/revoke"} {
		if rec := ts.do(t, "POST", p, url.Values{}, c); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", p, rec.Code)
		}
	}
}

func TestAuditPageNewestFirst(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	ts.clock.Add(time.Minute)
	ts.do(t, "POST", "/notes", noteForm("other", "hello"), c)
	page := ts.do(t, "GET", "/audit", nil, c).Body.String()
	created, enrolled := strings.Index(page, "note_created"), strings.Index(page, "passkey_enrolled")
	if created < 0 || enrolled < 0 || created > enrolled {
		t.Fatalf("order wrong (%d, %d): %s", created, enrolled, page)
	}
	// t0 is 09:00 UTC = 11:00 in Europe/Bratislava.
	if !strings.Contains(page, "2026-10-23 11:00:00") || strings.Contains(page, "09:00:00") {
		t.Fatalf("times not in timezone: %s", page)
	}
	for _, h := range []string{"Time", "Lane", "Actor", "Client", "Event", "Target", "Outcome"} {
		if !strings.Contains(page, "<th>"+h+"</th>") {
			t.Errorf("no column %s", h)
		}
	}
}

func TestNotesEscapeAgentText(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	ts.addNote(t, store.NoteOther, `<script>alert(1)</script>`, "agent:private:<b>x</b>")
	page := ts.do(t, "GET", "/notes", nil, c).Body.String()
	if strings.Contains(page, "<script>alert") || strings.Contains(page, "<b>x</b>") {
		t.Fatalf("unescaped: %s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(page, "written by the agent") {
		t.Fatalf("agent text not escaped and labelled: %s", page)
	}
}

// noteCard returns the part of the notes page that is the card of note id:
// from its <div class="card"> to the next card or the add form.
func noteCard(t *testing.T, page string, id int64) string {
	t.Helper()
	action := `action="/notes/` + strconv.FormatInt(id, 10) + `"`
	for _, part := range strings.Split(page, `<div class="card">`)[1:] {
		if strings.Contains(part, action) {
			card, _, _ := strings.Cut(part, "<h2>Add a note</h2>")
			return card
		}
	}
	t.Fatalf("no card for note %d: %s", id, page)
	return ""
}

func noteText(t *testing.T, ts *testServer, id int64) string {
	t.Helper()
	ns, err := ts.db.ListNotes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if n.ID == id {
			return n.Text
		}
	}
	t.Fatalf("note %d not found", id)
	return ""
}

// Browsers submit a textarea's line breaks as CRLF; they are stored as LF.
func TestNotesMultiLine(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	rec := ts.do(t, "POST", "/notes", noteForm("injury", "a\r\nb"), c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	ns, err := ts.db.ListNotes(context.Background())
	if err != nil || len(ns) != 1 || ns[0].Text != "a\nb" {
		t.Fatalf("notes = %+v, %v", ns, err)
	}
	// An agent's multi-line note comes back from the form as CRLF; a lone
	// CR is a line break too.
	a := ts.addNote(t, store.NoteOther, "line one\nline two", "agent:private:claude")
	for text, want := range map[string]string{"line one\r\nline two\r\nthree": "line one\nline two\nthree", "x\ry\r": "x\ny\n"} {
		rec := ts.do(t, "POST", "/notes/"+strconv.FormatInt(a.ID, 10), noteForm("other", text), c)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("edit %q: %d %s", text, rec.Code, rec.Body)
		}
		if got := noteText(t, ts, a.ID); got != want {
			t.Fatalf("edit %q stored %q, want %q", text, got, want)
		}
	}
}

func TestNotesWhitespaceOnly(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	const msg = "text must not be empty"
	rec := ts.do(t, "POST", "/notes", noteForm("other", " \r\n\t "), c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), msg) {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if ns, _ := ts.db.ListNotes(context.Background()); len(ns) != 0 {
		t.Fatalf("whitespace note stored: %+v", ns)
	}
	n := ts.addNote(t, store.NoteOther, "keep", "runner")
	rec = ts.do(t, "POST", "/notes/"+strconv.FormatInt(n.ID, 10), noteForm("other", "   "), c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), msg) {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if got := noteText(t, ts, n.ID); got != "keep" {
		t.Fatalf("note = %q", got)
	}
	if len(ts.audit(t, "note_created")) != 0 || len(ts.audit(t, "note_updated")) != 0 {
		t.Fatal("rejected writes were audited")
	}
}

// A rejected edit shows that note's form again with what the runner typed
// and the error beside it, so the edit is not lost.
func TestNoteEditRejectedKeepsTypedText(t *testing.T) {
	ts := newTestServer(t)
	c := ts.enroll(t)
	n := ts.addNote(t, store.NoteInjury, "stored text", "runner")
	other := ts.addNote(t, store.NoteOther, "other note", "runner")
	typed := strings.Repeat("a", store.MaxNoteRunes) + " <typed>"
	rec := ts.do(t, "POST", "/notes/"+strconv.FormatInt(n.ID, 10), noteForm("availability", typed), c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	page := rec.Body.String()
	card := noteCard(t, page, n.ID)
	if !strings.Contains(card, `<p class="alert">text: 508 characters exceeds the 500 limit`) {
		t.Errorf("the note's card lacks the error: %s", card)
	}
	if !strings.Contains(card, ">"+strings.Repeat("a", store.MaxNoteRunes)+" &lt;typed&gt;</textarea>") {
		t.Errorf("the note's form lacks the typed text: %s", card)
	}
	if !strings.Contains(card, `<option value="availability" selected>`) {
		t.Errorf("the note's form lacks the typed category: %s", card)
	}
	if !strings.Contains(card, "<p>stored text</p>") {
		t.Errorf("the card no longer shows the stored text: %s", card)
	}
	if strings.Contains(noteCard(t, page, other.ID), `<p class="alert">`) {
		t.Error("another note's card shows the error")
	}
	if top, _, _ := strings.Cut(page, `<div class="card">`); strings.Contains(top, `<p class="alert">`) {
		t.Errorf("the error is also shown at the top: %s", top)
	}
	if got := noteText(t, ts, n.ID); got != "stored text" {
		t.Fatalf("note = %q", got)
	}
}

func TestNotesValidUntilShown(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	soon, past := ts.today().AddDays(3), ts.today().AddDays(-1)
	for _, n := range []store.Note{
		{Category: store.NoteAvailability, Text: "trip ended", CreatedBy: "agent:public:claude", ValidUntil: &past},
		{Category: store.NoteAvailability, Text: "travelling", CreatedBy: "agent:public:claude", ValidUntil: &soon},
		{Category: store.NoteAvailability, Text: "no Tuesdays", CreatedBy: "runner"},
	} {
		n.CreatedAt, n.UpdatedAt = ts.clock.Now(), ts.clock.Now()
		if _, err := ts.db.InsertNote(ctx, n, store.MaxNotes); err != nil {
			t.Fatal(err)
		}
	}
	body := ts.do(t, "GET", "/notes", nil, nil).Body.String()
	if !strings.Contains(body, "until "+day(soon)) || !strings.Contains(body, "until "+day(past)) {
		t.Fatalf("notes page lacks the dates: %s", body)
	}
	if strings.Count(body, `<span class="pill">expired</span>`) != 1 || strings.Count(body, `class="card expired"`) != 1 {
		t.Fatalf("expected one expired note: %s", body)
	}
	// The expired note sorts after the live ones of its category.
	if i, j := strings.Index(body, "trip ended"), strings.Index(body, "no Tuesdays"); i < j {
		t.Fatal("the expired note is listed before a live one")
	}
}
