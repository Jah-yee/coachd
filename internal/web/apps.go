package web

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/idna"

	"github.com/stefiix92/coachd/internal/store"
)

const msgNoApp = "app connection not found"

// appRow is one connected app (spec §4.3). Web is the punycode host of a
// CIMD client_id, "" for a DCR client; Name is the app's own claim, made
// display-safe when it was stored.
type appRow struct {
	ID                    int64
	Web, Name             string
	RedirectHosts, Scopes string
	Created, LastUsed     string
}

type appsPage struct{ Apps []appRow }

// appsPage is GET /apps (builtin mode only): one row per grant with a live
// token.
func (s *Server) appsPage(w http.ResponseWriter, r *http.Request) {
	apps, err := s.d.DB.ConnectedApps(r.Context(), s.d.Now())
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	var p appsPage
	for _, a := range apps {
		row := appRow{ID: a.GrantID, Name: a.Name, RedirectHosts: redirectHosts(a.RedirectURIs),
			Scopes: strings.Join(a.Scopes, ", "), Created: a.CreatedAt.In(s.d.TZ).Format(auditLayout), LastUsed: "never"}
		if a.Kind == "cimd" {
			row.Web = webAddress(a.ClientID)
		}
		if a.LastUsedAt != nil {
			row.LastUsed = a.LastUsedAt.In(s.d.TZ).Format(auditLayout)
		}
		p.Apps = append(p.Apps, row)
	}
	s.render(w, r, http.StatusOK, "apps", p)
}

// appRevoke is POST /apps/{id}/revoke: it revokes the grant and with it
// every token; the store audits it with Actor runner.
func (s *Server) appRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoApp})
		return
	}
	err = s.d.DB.RevokeGrant(r.Context(), id, s.d.Now(), "runner")
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoApp})
	case err != nil:
		s.internalError(w, r, err, false)
	default:
		http.Redirect(w, r, "/apps", http.StatusSeeOther)
	}
}

// webAddress is a CIMD client_id's host in punycode, the form spec §5.2
// shows; "unknown" when it does not parse (the OAuth server validated it
// before storing, so that is not expected).
func webAddress(clientID string) string {
	u, err := url.Parse(clientID)
	if err != nil || u.Hostname() == "" {
		return "unknown"
	}
	h, err := idna.Lookup.ToASCII(u.Hostname())
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// redirectHosts lists the distinct hosts of a client's redirect URIs, in
// punycode (an IP literal as its address), in order.
func redirectHosts(uris []string) string {
	var hosts []string
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			continue
		}
		h := u.Hostname()
		if a, err := netip.ParseAddr(h); err == nil {
			h = a.String()
		} else if h, err = idna.Lookup.ToASCII(h); err != nil || h == "" {
			continue
		}
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return strings.Join(hosts, ", ")
}
