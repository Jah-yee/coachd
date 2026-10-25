package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/garmin"
	"github.com/stefiix92/coachd/internal/store"
)

// GarminKey is the Garmin key the page shows and renews. serve backs it
// with garminkey.Holder and the Garmin client.
type GarminKey interface {
	Expires() civil.Date
	// Renew checks key with Garmin and, when Garmin accepts it, stores it
	// and uses it from then on; on any error nothing changes.
	Renew(ctx context.Context, key string, expires civil.Date) error
}

// maxKeyBytes bounds a pasted key; a Garmin key is far shorter.
const maxKeyBytes = 4 << 10

// keySoonDays is when the home page starts warning that the key expires.
const keySoonDays = 14

const (
	msgKeyMissing     = "paste the new key"
	msgKeyMalformed   = "this does not look like a Garmin key: it has spaces or is too long"
	msgKeyValidity    = "choose how long the key is valid, as on Garmin's page"
	msgKeyRefused     = "Garmin refused this key; nothing changed"
	msgKeyRateLimited = "Garmin is limiting requests; try again later. Nothing changed"
	msgKeyUnreachable = "could not reach Garmin; nothing changed"
	msgKeyFailed      = "the key could not be checked or saved; nothing changed"
)

// keyValidity is one choice of how long a new key is valid, in days, as
// Garmin offers them.
type keyValidity struct {
	Days  int
	Label string
}

var keyValidities = []keyValidity{{30, "30 days"}, {90, "90 days"}, {180, "180 days"}, {365, "1 year"}}

// keyView is the key's expiry, for the Garmin page and the home page.
type keyView struct {
	Day, Countdown string
	Soon, Expired  bool
}

// garminPage is the data of garmin.html. It never holds the key.
type garminPage struct {
	Key        *keyView
	Validities []keyValidity
	Renewed    bool
	Error      string
}

// keyView describes the key's expiry today, or nil when the page has no key.
func (s *Server) keyView() *keyView {
	if s.d.GarminKey == nil {
		return nil
	}
	exp := s.d.GarminKey.Expires()
	today := civil.Of(s.d.Now(), s.d.TZ)
	left := exp.Sub(today)
	return &keyView{Day: day(exp) + " " + strconv.Itoa(exp.Year), Countdown: countdown(today, exp),
		Soon: left <= keySoonDays, Expired: left < 0}
}

func (s *Server) renderGarmin(w http.ResponseWriter, r *http.Request, status int, p garminPage) {
	p.Key = s.keyView()
	p.Validities = keyValidities
	s.render(w, r, status, "garmin", p)
}

// garminPageGet serves GET /garmin.
func (s *Server) garminPageGet(w http.ResponseWriter, r *http.Request) {
	s.renderGarmin(w, r, http.StatusOK, garminPage{Renewed: r.URL.Query().Has("renewed")})
}

// validKey reports whether key could be a Garmin key: present, short, and
// without spaces or control characters.
func validKey(key string) bool {
	return len(key) <= maxKeyBytes && strings.IndexFunc(key, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

// garminRenew serves POST /garmin behind requireSession: the runner pastes a
// new key. The key is never logged, audited or shown again.
func (s *Server) garminRenew(w http.ResponseWriter, r *http.Request) {
	if s.d.GarminKey == nil {
		s.notFound(w, r)
		return
	}
	key := strings.TrimSpace(r.PostFormValue("key"))
	days, err := strconv.Atoi(r.PostFormValue("valid_days"))
	valid := err == nil && days > 0
	if valid {
		valid = false
		for _, v := range keyValidities {
			valid = valid || v.Days == days
		}
	}
	switch {
	case key == "":
		s.renderGarmin(w, r, http.StatusBadRequest, garminPage{Error: msgKeyMissing})
		return
	case !validKey(key):
		s.renderGarmin(w, r, http.StatusBadRequest, garminPage{Error: msgKeyMalformed})
		return
	case !valid:
		s.renderGarmin(w, r, http.StatusBadRequest, garminPage{Error: msgKeyValidity})
		return
	}
	ctx := r.Context()
	now := s.d.Now()
	expires := civil.Of(now, s.d.TZ).AddDays(days)
	err = s.d.GarminKey.Renew(ctx, key, expires)
	outcome, status, msg := keyOutcome(err)
	ws, _ := s.session(r)
	if aerr := s.d.DB.AppendAudit(ctx, store.AuditEvent{At: now, Lane: "public", Actor: "runner",
		Client: client(r, ws.PasskeyID), Event: "garmin_key_renewed", Outcome: outcome}); aerr != nil {
		s.d.Log.ErrorContext(ctx, "audit write failed", "component", "web", "http.route", r.Pattern,
			"error_class", errorClass(aerr))
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "garmin key not renewed", "component", "web", "http.route", r.Pattern,
			"outcome", outcome, "error_class", errorClass(err))
		s.renderGarmin(w, r, status, garminPage{Error: msg})
		return
	}
	s.d.Log.InfoContext(ctx, "garmin key renewed", "component", "web", "http.route", r.Pattern,
		"outcome", outcome, "calendar_date", expires.String())
	http.Redirect(w, r, "/garmin?renewed", http.StatusSeeOther)
}

// keyOutcome maps a renewal's error to its audit outcome, status and message.
func keyOutcome(err error) (outcome string, status int, msg string) {
	var rl *garmin.RateLimitedError
	var se *garmin.StatusError
	switch {
	case err == nil:
		return "ok", http.StatusSeeOther, ""
	case errors.Is(err, garmin.ErrUnauthorized):
		return "refused", http.StatusBadRequest, msgKeyRefused
	case errors.As(err, &rl):
		return "rate_limited", http.StatusServiceUnavailable, msgKeyRateLimited
	case errors.As(err, &se) && se.Status == 0:
		return "unreachable", http.StatusBadGateway, msgKeyUnreachable
	}
	return "error", http.StatusBadGateway, msgKeyFailed
}
