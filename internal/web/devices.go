package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	msgNoPairing        = "no pending pairing with that code; codes expire after 10 minutes"
	msgNoDevice         = "device not found"
	maxDeviceLabelRunes = 40
)

type deviceRow struct {
	ID                        int64
	Label, Model              string
	Paired, LastSeen, Revoked string
	Active                    bool
}

// pendingRow is a pairing a watch started that nobody confirmed yet. It
// never carries the code: the runner must read it from the watch.
type pendingRow struct {
	Model, Started string
}

// devicesPage is the data of devices.html. Error and DraftCode/DraftLabel
// re-show a rejected pairing form.
type devicesPage struct {
	Devices               []deviceRow
	Pending               []pendingRow
	Error                 string
	DraftCode, DraftLabel string
	MaxRunes              int
}

func (s *Server) renderDevices(w http.ResponseWriter, r *http.Request, status int, p devicesPage) {
	ds, err := s.d.DB.ListDevices(r.Context())
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	p.MaxRunes = maxDeviceLabelRunes
	for _, d := range ds {
		row := deviceRow{ID: d.ID, Label: d.Label, Model: d.Model, Paired: d.CreatedAt.In(s.d.TZ).Format(auditLayout),
			LastSeen: "never", Active: d.RevokedAt == nil}
		if d.LastSeenAt != nil {
			row.LastSeen = d.LastSeenAt.In(s.d.TZ).Format(auditLayout)
		}
		if d.RevokedAt != nil {
			row.Revoked = d.RevokedAt.In(s.d.TZ).Format(auditLayout)
		}
		p.Devices = append(p.Devices, row)
	}
	now := s.d.Now()
	pending, err := s.d.DB.PendingPairings(r.Context(), now)
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	for _, pp := range pending {
		p.Pending = append(p.Pending, pendingRow{Model: pp.Model, Started: started(now.Sub(pp.CreatedAt))})
	}
	s.render(w, r, status, "devices", p)
}

// started describes a pairing's age: "started just now" under a minute,
// otherwise "started N min ago" (pairings expire after 10 minutes).
func started(age time.Duration) string {
	if age < time.Minute {
		return "started just now"
	}
	return "started " + strconv.Itoa(int(age/time.Minute)) + " min ago"
}

func (s *Server) devicesPage(w http.ResponseWriter, r *http.Request) {
	s.renderDevices(w, r, http.StatusOK, devicesPage{})
}

// validPairingCode reports whether c is exactly six ASCII digits.
func validPairingCode(c string) bool {
	if len(c) != 6 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < '0' || c[i] > '9' {
			return false
		}
	}
	return true
}

// devicePair serves POST /devices/pair behind requireSession: the runner
// confirms the code the watch shows.
func (s *Server) devicePair(w http.ResponseWriter, r *http.Request) {
	code, label := strings.TrimSpace(r.PostFormValue("code")), strings.TrimSpace(r.PostFormValue("label"))
	draft := devicesPage{DraftCode: code, DraftLabel: label}
	now := s.d.Now()
	if label == "" {
		label = "watch " + civil.Of(now, s.d.TZ).String()
	}
	var inv *workout.Invalid
	if err := workout.CheckLine("label", label, maxDeviceLabelRunes); errors.As(err, &inv) {
		draft.Error = inv.Reason
		s.renderDevices(w, r, http.StatusBadRequest, draft)
		return
	}
	if !validPairingCode(code) {
		draft.Error = msgNoPairing
		s.renderDevices(w, r, http.StatusBadRequest, draft)
		return
	}
	ws, _ := s.session(r)
	_, err := s.d.DB.ConfirmPairing(r.Context(), code, label, now, "public", "runner", client(r, ws.PasskeyID))
	switch {
	case errors.Is(err, store.ErrNotFound):
		draft.Error = msgNoPairing
		s.renderDevices(w, r, http.StatusBadRequest, draft)
	case err != nil:
		s.internalError(w, r, err, false)
	default:
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
	}
}

// deviceRevoke serves POST /devices/{id}/revoke behind requireSession.
func (s *Server) deviceRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoDevice})
		return
	}
	ws, _ := s.session(r)
	err = s.d.DB.RevokeDevice(r.Context(), id, s.d.Now(), "public", "runner", client(r, ws.PasskeyID))
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoDevice})
	case err != nil:
		s.internalError(w, r, err, false)
	default:
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
	}
}
