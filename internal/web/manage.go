package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	auditRows   = 200
	auditLayout = "2006-01-02 15:04:05"
	msgNoNote   = "note not found"
	msgNoKey    = "passkey not found"
	msgCategory = "category must be one of injury, availability, preference, other"
	msgNoteText = "text must not be empty"
)

var noteCategories = []store.NoteCategory{store.NoteInjury, store.NoteAvailability, store.NotePreference, store.NoteOther}

// noteRow is one note on the page. Text is agent-written when ByAgent.
// FormCategory and FormText fill its edit form: the stored values, or what
// the runner typed in a rejected edit, which Error then explains.
type noteRow struct {
	ID                     int64
	Category               string
	Text                   string
	Author                 string
	ByAgent                bool
	Updated                string
	Until                  string // the last day the note holds; empty for a durable note
	Expired                bool
	FormCategory, FormText string
	Error                  string
}

// notesPage is the data of notes.html. Error and the Draft fields re-show a
// rejected create, or with EditID a rejected edit of that note: its form
// then shows the draft and the error instead of the page's top.
type notesPage struct {
	Notes         []noteRow
	Categories    []store.NoteCategory
	Count, Max    int
	Error         string
	DraftCategory string
	DraftText     string
	EditID        int64
	MaxRunes      int
}

// author is how a note's created_by reads on the page.
func author(createdBy string) (label string, agent bool) {
	if createdBy == "runner" {
		return "you", false
	}
	parts := strings.SplitN(createdBy, ":", 3)
	if len(parts) == 3 && parts[0] == "agent" && parts[2] != "" {
		return "agent (" + parts[2] + ")", true
	}
	return "agent", true
}

func (s *Server) renderNotes(w http.ResponseWriter, r *http.Request, status int, p notesPage) {
	ns, err := s.d.DB.ListNotes(r.Context())
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	p.Categories, p.Count, p.Max, p.MaxRunes = noteCategories, len(ns), store.MaxNotes, store.MaxNoteRunes
	today := civil.Of(s.d.Now(), s.d.TZ)
	for _, n := range ns {
		a, byAgent := author(n.CreatedBy)
		row := noteRow{ID: n.ID, Category: string(n.Category), Text: n.Text, Author: a, ByAgent: byAgent,
			Updated: n.UpdatedAt.In(s.d.TZ).Format(auditLayout), FormCategory: string(n.Category), FormText: n.Text,
			Expired: n.Expired(today)}
		if n.ValidUntil != nil {
			row.Until = day(*n.ValidUntil)
		}
		if p.EditID != 0 && n.ID == p.EditID {
			row.FormText, row.Error = p.DraftText, p.Error
			if validCategory(store.NoteCategory(p.DraftCategory)) {
				row.FormCategory = p.DraftCategory
			}
			p.Error = "" // shown beside the note instead
		}
		p.Notes = append(p.Notes, row)
	}
	// Within a category, expired notes follow the live ones.
	slices.SortStableFunc(p.Notes, func(a, b noteRow) int {
		if c := strings.Compare(a.Category, b.Category); c != 0 {
			return c
		}
		switch {
		case a.Expired == b.Expired:
			return 0
		case a.Expired:
			return 1
		}
		return -1
	})
	s.render(w, r, status, "notes", p)
}

func (s *Server) notesPage(w http.ResponseWriter, r *http.Request) {
	s.renderNotes(w, r, http.StatusOK, notesPage{})
}

// lineBreaks turns CRLF and lone CR into LF: browsers submit a textarea's
// line breaks as CRLF, and CheckText allows only LF.
var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// noteInput reads and checks the category and text of a note form, with
// line breaks normalised to LF. It returns the message to show when they are
// invalid.
func noteInput(r *http.Request) (store.NoteCategory, string, string) {
	cat, text := store.NoteCategory(r.PostFormValue("category")), lineBreaks.Replace(r.PostFormValue("text"))
	if !validCategory(cat) {
		return cat, text, msgCategory
	}
	var inv *workout.Invalid
	if err := workout.CheckText("text", text, store.MaxNoteRunes); errors.As(err, &inv) {
		return cat, text, inv.Reason
	}
	if strings.TrimSpace(text) == "" {
		return cat, text, msgNoteText
	}
	return cat, text, ""
}

func validCategory(c store.NoteCategory) bool {
	for _, k := range noteCategories {
		if c == k {
			return true
		}
	}
	return false
}

func (s *Server) noteAudit(r *http.Request, event string, id int64) {
	ws, _ := s.session(r)
	s.appendAudit(r, store.AuditEvent{At: s.d.Now(), Lane: "public", Actor: "runner", Client: client(r, ws.PasskeyID),
		Event: event, Target: strconv.FormatInt(id, 10), Outcome: "ok"})
}

func (s *Server) noteCreate(w http.ResponseWriter, r *http.Request) {
	cat, text, msg := noteInput(r)
	if msg != "" {
		s.renderNotes(w, r, http.StatusBadRequest, notesPage{Error: msg, DraftCategory: string(cat), DraftText: text})
		return
	}
	now := s.d.Now()
	n, err := s.d.DB.InsertNote(r.Context(), store.Note{Category: cat, Text: text, CreatedBy: "runner", CreatedAt: now, UpdatedAt: now}, store.MaxNotes)
	if errors.Is(err, store.ErrNotesFull) {
		s.renderNotes(w, r, http.StatusBadRequest, notesPage{
			Error:         fmt.Sprintf("notes are full (%d of %d); delete one first", store.MaxNotes, store.MaxNotes),
			DraftCategory: string(cat), DraftText: text})
		return
	}
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	s.noteAudit(r, "note_created", n.ID)
	http.Redirect(w, r, "/notes", http.StatusSeeOther)
}

// noteID parses the {id} path value; on failure it answers 404.
func (s *Server) noteID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoNote})
		return 0, false
	}
	return id, true
}

func (s *Server) noteNotFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoNote})
}

func (s *Server) noteEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.noteID(w, r)
	if !ok {
		return
	}
	cat, text, msg := noteInput(r)
	if msg != "" {
		s.renderNotes(w, r, http.StatusBadRequest, notesPage{Error: msg, EditID: id, DraftCategory: string(cat), DraftText: text})
		return
	}
	_, err := s.d.DB.UpdateRunnerNote(r.Context(), store.Note{ID: id, Category: cat, Text: text, UpdatedAt: s.d.Now()})
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.noteNotFound(w, r)
	case err != nil:
		s.internalError(w, r, err, false)
	default:
		s.noteAudit(r, "note_updated", id)
		http.Redirect(w, r, "/notes", http.StatusSeeOther)
	}
}

func (s *Server) noteDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.noteID(w, r)
	if !ok {
		return
	}
	err := s.d.DB.DeleteNote(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.noteNotFound(w, r)
	case err != nil:
		s.internalError(w, r, err, false)
	default:
		s.noteAudit(r, "note_deleted", id)
		http.Redirect(w, r, "/notes", http.StatusSeeOther)
	}
}

type passkeyRow struct {
	ID                       int64
	Label, Created, LastUsed string
	Revoked                  string
	Active, Current          bool
}

type passkeysPage struct{ Keys []passkeyRow }

func (s *Server) passkeysPage(w http.ResponseWriter, r *http.Request) {
	pks, err := s.d.DB.ListPasskeys(r.Context())
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	ws, signedIn := s.session(r)
	var p passkeysPage
	for _, k := range pks {
		row := passkeyRow{ID: k.ID, Label: k.Label, Created: k.CreatedAt.In(s.d.TZ).Format(auditLayout),
			Active: k.RevokedAt == nil, Current: signedIn && ws.PasskeyID == k.ID, LastUsed: "never"}
		if k.LastUsedAt != nil {
			row.LastUsed = k.LastUsedAt.In(s.d.TZ).Format(auditLayout)
		}
		if k.RevokedAt != nil {
			row.Revoked = k.RevokedAt.In(s.d.TZ).Format(auditLayout)
		}
		p.Keys = append(p.Keys, row)
	}
	s.render(w, r, http.StatusOK, "passkeys", p)
}

func (s *Server) passkeyRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoKey})
		return
	}
	ws, _ := s.session(r)
	err = s.d.DB.RevokePasskey(r.Context(), id, s.d.Now(), "public", client(r, ws.PasskeyID))
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoKey})
	case err != nil:
		s.internalError(w, r, err, false)
	case ws.PasskeyID == id:
		// The store already deleted the session row; clear the cookie.
		if err := s.endSession(w, r); err != nil {
			s.internalError(w, r, err, false)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/passkeys", http.StatusSeeOther)
	}
}

type auditRow struct{ Time, Lane, Actor, Client, Event, Target, Outcome string }

type auditPage struct{ Rows []auditRow }

func (s *Server) auditPage(w http.ResponseWriter, r *http.Request) {
	evs, err := s.d.DB.ListAudit(r.Context(), auditRows)
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	var p auditPage
	for _, e := range evs {
		p.Rows = append(p.Rows, auditRow{Time: e.At.In(s.d.TZ).Format(auditLayout), Lane: e.Lane, Actor: e.Actor,
			Client: e.Client, Event: e.Event, Target: e.Target, Outcome: e.Outcome})
	}
	s.render(w, r, http.StatusOK, "audit", p)
}
