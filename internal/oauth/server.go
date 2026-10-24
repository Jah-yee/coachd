package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stefiix92/coachd/internal/config"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// maxBody caps every request body.
const maxBody = 64 << 10

// Notifier tells the runner about connection events (notify.Ntfy).
type Notifier interface {
	AppConnected()
	ConnectionRevoked()
}

// Deps are the server's collaborators.
type Deps struct {
	DB        *store.DB
	NonceKey  []byte // at least 32 bytes; the request and DCR keys derive from it
	PublicURL string // COACHD_PUBLIC_URL
	DCR       bool   // COACHD_OAUTH_DCR=on
	Now       func() time.Time
	Log       *slog.Logger
	Metrics   *telemetry.Metrics // may be nil
	Notifier  Notifier           // may be nil
}

// Server is the OAuth authorization server.
type Server struct {
	d          Deps
	self       string // the issuer: PublicURL normalised
	res        *resolver
	requestKey []byte
	prm, as    []byte // the rendered metadata documents
}

// New builds the server. Production code never injects fetcher options.
func New(d Deps) (*Server, error) { return newServer(d, fetcherOpts{}) }

func newServer(d Deps, o fetcherOpts) (*Server, error) {
	if d.DB == nil {
		return nil, errors.New("oauth: DB is required")
	}
	if len(d.NonceKey) < sha256.Size {
		return nil, errors.New("oauth: NonceKey must be at least 32 bytes")
	}
	self, err := config.ParsePublicURL(d.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("oauth: %w", err)
	}
	if self == "" {
		return nil, errors.New("oauth: PublicURL is required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	s := &Server{d: d, self: self, requestKey: deriveKey(d.NonceKey, labelRequest)}
	s.res = newResolver(d.DB, self, deriveKey(d.NonceKey, labelDCR))
	s.res.now = d.Now
	s.res.fetchDoc = newFetcher(o).fetch
	if s.prm, s.as, err = buildMetadata(self, d.DCR); err != nil {
		return nil, fmt.Errorf("oauth: metadata: %w", err)
	}
	return s, nil
}

// Handler serves the metadata, registration and revocation endpoints; every
// other path is a JSON 404. It is meant for the public mux, outside the page
// handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	doc := func(d []byte) map[string]http.HandlerFunc {
		return map[string]http.HandlerFunc{http.MethodGet: serveDoc(d)}
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.route("metadata", doc(s.prm)))
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.route("metadata", doc(s.prm)))
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.route("metadata", doc(s.as)))
	if s.d.DCR {
		mux.HandleFunc("/oauth/register", s.route("register", map[string]http.HandlerFunc{http.MethodPost: s.register}))
	}
	mux.HandleFunc("/oauth/revoke", s.route("revoke", map[string]http.HandlerFunc{http.MethodPost: s.revoke}))
	mux.HandleFunc("/oauth/token", noStore(s.route("token", map[string]http.HandlerFunc{http.MethodPost: s.token})))
	mux.HandleFunc("/", s.instrument("other", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	}))
	return mux
}

// route sets the CORS headers, answers the preflight and refuses other
// methods, then runs the method's handler under instrument.
func (s *Server) route(endpoint string, methods map[string]http.HandlerFunc) http.HandlerFunc {
	return s.instrument(endpoint, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			h := w.Header()
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			h.Set("Access-Control-Max-Age", "3600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h, ok := methods[r.Method]
		if !ok {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		h(w, r)
	})
}

// instrument sets the CORS origin and counts the request by endpoint and
// outcome: the one the handler set with setOutcome, else the final status's.
func (s *Server) instrument(endpoint string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		rec := &statusRecorder{ResponseWriter: w}
		h(rec, r)
		outcome := rec.outcome
		if outcome == "" {
			outcome = outcomeOf(rec.code())
		}
		s.count(r.Context(), endpoint, outcome)
	}
}

// outcomes is the §9 set of coachd.oauth.requests outcome values.
var outcomes = []string{"ok", "invalid_request", "invalid_client", "invalid_grant", "invalid_scope", "invalid_target",
	"access_denied", "reuse", "rate_limited", "fetch_failed", "login_required", "not_found", "error"}

// CountConsent counts a consent request that never reached Begin, Approve or
// Deny (a failed passkey assertion, a rate-limited or malformed request),
// which count themselves. outcome must be one of the §9 values; anything
// else is ignored, so a label never carries request data.
func (s *Server) CountConsent(ctx context.Context, outcome string) {
	if slices.Contains(outcomes, outcome) {
		s.count(ctx, "consent", outcome)
	}
}

// count adds one to coachd.oauth.requests. A nil Metrics counts nothing.
func (s *Server) count(ctx context.Context, endpoint, outcome string) {
	m := s.d.Metrics
	if m == nil || m.OAuthRequests == nil {
		return
	}
	m.OAuthRequests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("endpoint", endpoint), attribute.String("outcome", outcome)))
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	outcome string // set by the handler when the status alone is ambiguous
}

// setOutcome records the §9 outcome of a request whose status alone does not
// tell it (every token endpoint error is a 400). It is one of the enumerated
// outcome constants, never request data. Outside instrument it does nothing.
func setOutcome(w http.ResponseWriter, outcome string) {
	if r, ok := w.(*statusRecorder); ok {
		r.outcome = outcome
	}
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) code() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func outcomeOf(status int) string {
	switch {
	case status < 400:
		return "ok"
	case status == http.StatusNotFound, status == http.StatusMethodNotAllowed:
		return "not_found"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status < 500:
		return "invalid_request"
	}
	return "error"
}

func errorClass(err error) string { return fmt.Sprintf("%T", err) }

func (s *Server) logFail(ctx context.Context, endpoint string, err error) {
	s.d.Log.LogAttrs(ctx, slog.LevelError, "oauth request failed",
		slog.String("component", "oauth"), slog.String("endpoint", endpoint),
		slog.String("outcome", "error"), slog.String("error_class", errorClass(err)))
}

// writeJSON writes v with the status. Token and registration responses are never cached.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

// tooLarge reports whether err came from the body cap.
func tooLarge(err error) bool {
	var mb *http.MaxBytesError
	return errors.As(err, &mb)
}

// register is RFC 7591 dynamic client registration. Nothing is stored.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if tooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_client_metadata")
		return
	}
	var req registration
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_client_metadata")
		return
	}
	_, resp, err := registerDCR(s.res.dcrKey, req, s.self, s.d.Now())
	if err != nil {
		if errors.Is(err, errRedirectURI) {
			writeError(w, http.StatusBadRequest, "invalid_redirect_uri")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_client_metadata")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// revoke is RFC 7009. A token that is unknown, expired or another client's
// revokes nothing and still answers 200.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	form, err := parseForm(r)
	if err != nil {
		if tooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	token := form.Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := s.d.DB.RevokeGrantByToken(r.Context(), hashToken(token), form.Get("client_id"), s.d.Now()); err != nil {
		s.logFail(r.Context(), "revoke", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// errForm is a token or revocation request body that is not a form, or that
// repeats a parameter (RFC 6749 §3.1: none may be included more than once).
var errForm = errors.New("oauth: malformed form")

// parseForm reads an application/x-www-form-urlencoded body, ignoring the
// query string. Another content type or a repeated parameter is refused. The
// caller caps r.Body.
func parseForm(r *http.Request) (url.Values, error) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/x-www-form-urlencoded" {
		return nil, errForm
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, errForm
	}
	for _, v := range form {
		if len(v) > 1 {
			return nil, errForm
		}
	}
	return form, nil
}
