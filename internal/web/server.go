// Package web serves the runner's pages: sign-in and enrolment with a
// passkey, the home page, proposals, notes, passkeys, devices, the audit log
// and, in builtin mode, the OAuth consent and Connected apps pages. It is
// mounted on the public lane only. In Access mode it sits behind the
// Cloudflare Access middleware and refuses any request without a public-lane
// identity; in builtin mode (no Access in front) a default-deny session gate
// lets only the sign-in routes through without a passkey session.
//
// Ruling R59: the Access identity alone cannot tell the runner from an agent
// using Managed OAuth, so every state-changing POST also needs a passkey,
// either a fresh assertion bound to the action or a passkey session.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/time/rate"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/oauth"
	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/app.js static/app.css static/favicon.svg static/favicon.png static/icon.png
var staticFS embed.FS

// Deps are the server's collaborators.
type Deps struct {
	DB       *store.DB
	Passkeys *passkey.Service
	TZ       *time.Location // COACHD_TIMEZONE: dates on the pages
	Now      func() time.Time
	Log      *slog.Logger
	Metrics  *telemetry.Metrics // may be nil
	Tracer   trace.Tracer       // may be nil
	OAuth    *oauth.Server      // may be nil: no consent routes (Access mode)
	// Builtin selects builtin mode (COACHD_PUBLIC_AUTH=builtin): the session
	// gate replaces publicOnly and /apps is served.
	Builtin bool
	// Sampler samples failure audit rows (spec §6); nil audits every failure.
	Sampler *auth.AuditSampler
	// GarminKey shows and renews the Garmin key; nil means the page offers
	// no renewal.
	GarminKey GarminKey
}

// Server is the runner's web page.
type Server struct {
	d     Deps
	pages map[string]*template.Template
	// Failure buckets of the login, enrolment and consent finishes
	// (failureBucket); tests may replace them before serving.
	loginFails, enrollFails, consentFails *rate.Limiter
	// Log limiters for the paths a stranger can repeat at will (fail's 429
	// branch, tokenMiss): the metric counts every request, the log line is
	// bounded so a flood cannot fill the log store. Tests may replace them.
	limitedLog, tokenMissLog *rate.Limiter
}

const (
	// maxBody caps every request body.
	maxBody = 64 << 10
	csp     = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
		"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	layoutFile = "templates/layout.html"
)

// New checks d and parses the embedded templates: each page is
// layout.html plus templates/<page>.html.
func New(d Deps) (*Server, error) {
	if d.DB == nil || d.Passkeys == nil || d.TZ == nil {
		return nil, errors.New("web: DB, Passkeys and TZ are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Tracer == nil {
		d.Tracer = noop.NewTracerProvider().Tracer("")
	}
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: templates: %w", err)
	}
	pages := map[string]*template.Template{}
	for _, f := range files {
		if f == layoutFile {
			continue
		}
		name := strings.TrimSuffix(path.Base(f), ".html")
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, layoutFile, f)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", f, err)
		}
		pages[name] = t
	}
	return &Server{d: d, pages: pages,
		loginFails: newFailureBucket(), enrollFails: newFailureBucket(), consentFails: newFailureBucket(),
		limitedLog:   rate.NewLimiter(rate.Every(time.Minute), 1),
		tokenMissLog: rate.NewLimiter(rate.Every(2*time.Second), 10)}, nil
}

var funcs = template.FuncMap{
	// plural returns "1 warning" or "n warnings".
	"plural": func(n int, word string) string {
		if n == 1 {
			return "1 " + word
		}
		return fmt.Sprintf("%d %ss", n, word)
	},
	// statusLabel shows a proposal status as words: "delivery failed".
	"statusLabel": func(status string) string { return strings.ReplaceAll(status, "_", " ") },
	// tab is a navigation link of the layout, current when it is the
	// requested page.
	"tab": func(path, href, label string) navTab { return navTab{Href: href, Label: label, Current: path == href} },
}

// navTab is one link of the layout's navigation.
type navTab struct {
	Href, Label string
	Current     bool
}

// Handler returns the page handler. Order: security headers, the gate
// (builtin mode: the session gate; Access mode: the public-lane identity
// check), cross-origin protection for unsafe methods, body cap, mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	var h http.Handler = mux
	h = limitBody(h)
	h = http.NewCrossOriginProtection().Handler(h)
	if s.d.Builtin {
		h = s.sessionGate(h)
	} else {
		h = s.publicOnly(h)
	}
	return securityHeaders(h)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// publicOnly fails closed: a request without a public-lane identity from
// the Access middleware gets 403, so a mis-mounted page serves nothing.
func (s *Server) publicOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := auth.IdentityFrom(r.Context()); !ok || id.Lane != auth.LanePublic {
			s.d.Log.WarnContext(r.Context(), "web request without a public-lane identity", "component", "web", "outcome", "forbidden")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody refuses a declared body over maxBody and caps the reader for
// the rest (chunked bodies).
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBody {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// staticTypes lists the embedded assets with fixed content types, so they do
// not depend on the host's MIME tables.
var staticTypes = map[string]string{
	"app.js":      "text/javascript; charset=utf-8",
	"app.css":     "text/css; charset=utf-8",
	"favicon.svg": "image/svg+xml", // the logo on a white tile, for browser tabs
	"favicon.png": "image/png",     // the same, for browsers without SVG icons
	"icon.png":    "image/png",     // the home-screen icon
}

// static serves the embedded assets, cacheable for an hour.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	ctype, ok := staticTypes[name]
	if !ok {
		s.notFound(w, r)
		return
	}
	b, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: "page not found"})
}

// errorPage is the data of error.html.
type errorPage struct {
	Title, Message string
	Login          bool // offer a link to /login
}

// view is what every page template gets: the layout's state plus the page data.
type view struct {
	SignedIn bool
	Builtin  bool   // builtin mode: the layout links Connected apps
	Name     string // the page template, for page-specific layout
	Path     string // the request path, to mark the current navigation link
	Page     any
}

// render executes page name into a buffer first, so a template error is a
// clean 500 rather than half a page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	t, ok := s.pages[name]
	if !ok {
		s.internalError(w, r, fmt.Errorf("web: no page %q", name), false)
		return
	}
	_, signedIn := s.session(r)
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", view{SignedIn: signedIn, Builtin: s.d.Builtin, Name: name, Path: r.URL.Path, Page: data}); err != nil {
		s.internalError(w, r, err, false)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// internalError logs only the error's type (its text may carry values) and
// answers 500.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error, asJSON bool) {
	s.d.Log.ErrorContext(r.Context(), "web request failed", "component", "web", "http.route", r.Pattern,
		"outcome", "error", "error_class", errorClass(err))
	if asJSON {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// errorClass is the error's Go type: a low-cardinality label that, unlike
// the error text, never carries values.
func errorClass(err error) string { return fmt.Sprintf("%T", err) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// isJSON reports whether the request body is declared as JSON.
func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// decodeJSON reads a JSON body into dst. It answers 413 past the body cap
// and 400 for anything else that is not one JSON object of the right shape.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !isJSON(r) {
		writeError(w, http.StatusBadRequest, "malformed request")
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "malformed request")
		return false
	}
	return true
}

// client is the audit client of a request: the passkey when known, else
// the user-agent class the Access middleware recorded, else (builtin mode,
// where no middleware runs before the page) the request's own user-agent
// class.
func client(r *http.Request, passkeyID int64) string {
	if passkeyID > 0 {
		return fmt.Sprintf("passkey:%d", passkeyID)
	}
	if id, ok := auth.IdentityFrom(r.Context()); ok && id.Client != "" {
		return id.Client
	}
	return telemetry.ClientClass(r.UserAgent())
}
