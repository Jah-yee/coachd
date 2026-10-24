// Package deviceapi serves /device/v1/*, the HTTP API the Connect IQ watch
// app talks to: pairing, the desired set of approved workouts, FIT download
// and delivery acknowledgement.
//
// Ruling R-M3-1: it is mounted on the public listener outside the Cloudflare
// Access middleware, so every request is hostile internet traffic until the
// device bearer token checks out. The pairing endpoints are open; everything
// else needs a token issued after the runner confirmed the pairing code.
//
// No agent path to the watch: this API can only read the desired set,
// download FIT files of workouts already in it and acknowledge them. It
// never approves, declines, withdraws or changes a proposal.
package deviceapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// Deps are the server's collaborators.
type Deps struct {
	DB      *store.DB
	TZ      *time.Location // COACHD_TIMEZONE: today and the desired-set window
	Now     func() time.Time
	Log     *slog.Logger
	Metrics *telemetry.Metrics // may be nil
	Tracer  trace.Tracer       // may be nil
}

// Server is the device API.
type Server struct {
	d          Deps
	devices    *auth.Limiter // per device token
	pairCreate *auth.Limiter // one global bucket for POST /device/v1/pairings
	pairPoll   *auth.Limiter // one global bucket for GET /device/v1/pairings/{id}
}

const (
	maxBody = 4 << 10 // every request body

	// Global pairing rate limits, per minute and burst. Polling allows two
	// watches polling every 3 s at once.
	pairCreatePerMin, pairCreateBurst = 10, 5
	pairPollPerMin, pairPollBurst     = 60, 30

	laneDevice  auth.Lane = "device"
	lanePairing auth.Lane = "pairing"

	msgMalformed    = "malformed request"
	msgNotPaired    = "device not paired"
	msgNotFound     = "not found"
	msgTooMany      = "too many requests"
	msgTooManyPairs = "too many pending pairings"
	msgInternal     = "internal error"
)

// New returns the server. DB and TZ are required; Now, Log and Tracer default
// to the wall clock, a discarding logger and a no-op tracer.
func New(d Deps) *Server {
	if d.DB == nil || d.TZ == nil {
		panic("deviceapi: DB and TZ are required")
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
	return &Server{d: d, devices: auth.NewLimiter(60, 70),
		pairCreate: auth.NewLimiter(pairCreatePerMin, pairCreateBurst), pairPoll: auth.NewLimiter(pairPollPerMin, pairPollBurst)}
}

// Handler serves only /device/v1/...; any other path or method is a JSON 404.
// Order: response headers, canonical request check, body cap, routes. Each
// route applies its own rate limit and, for workouts, the device token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/v1/pairings", s.instrument("pairing_create", pairingLimit(s.pairCreate, s.createPairing)))
	mux.HandleFunc("GET /device/v1/pairings/{id}", s.instrument("pairing_poll", pairingLimit(s.pairPoll, s.pollPairing)))
	mux.HandleFunc("GET /device/v1/workouts", s.instrument("workouts", s.requireDevice(s.desired)))
	mux.HandleFunc("GET /device/v1/workouts/{id}/fit", s.instrument("workout_fit", s.requireDevice(s.fit)))
	mux.HandleFunc("POST /device/v1/workouts/{id}/ack", s.instrument("workout_ack", s.requireDevice(s.ack)))
	mux.HandleFunc("/", s.instrument("unknown", notFound))
	return headers(canonical(limitBody(mux)))
}

// instrument counts the request under endpoint (a fixed route name, never a
// path) and the outcome of the final status. It wraps each route outermost, so
// rate limit and auth refusals are counted. Requests canonical or limitBody
// already refused never reach a route and are not counted.
func (s *Server) instrument(endpoint string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := s.d.Metrics
		if m == nil || m.DeviceRequests == nil {
			h(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		h(rec, r)
		m.DeviceRequests.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("endpoint", endpoint), attribute.String("outcome", outcomeOf(rec.code()))))
	}
}

// statusRecorder remembers the response status. A body written without
// WriteHeader counts as 200.
type statusRecorder struct {
	http.ResponseWriter
	status int
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

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) code() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// outcomeOf maps a final status to the low-cardinality outcome label.
func outcomeOf(status int) string {
	switch {
	case status < 400:
		return "ok"
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status < 500:
		return "bad_request"
	}
	return "error"
}

// headers sets the headers every response carries. The FIT handler replaces
// Content-Type.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// canonical answers 404 to a non-canonical path (dot segments, double
// slashes), which the mux would answer with an HTML redirect, and to HEAD
// and CONNECT. HEAD would otherwise run the GET handlers, and a HEAD of a
// FIT download would record a fetch the watch never receives.
func canonical(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.EscapedPath()
		clean := path.Clean(p)
		if strings.HasSuffix(p, "/") && clean != "/" {
			clean += "/"
		}
		if r.Method == http.MethodHead || r.Method == http.MethodConnect || !strings.HasPrefix(p, "/") || clean != p {
			notFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody refuses a declared body over maxBody and caps the reader for the
// rest (chunked bodies); readBody reports the overflow.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBody {
			writeError(w, http.StatusBadRequest, msgMalformed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// readBody reads the whole (capped) body.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(r.Body)
}

// pairingLimit applies a global pairing bucket before any other work.
func pairingLimit(l *auth.Limiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(lanePairing, "global") {
			tooMany(w, msgTooMany)
			return
		}
		next(w, r)
	}
}

type deviceKey struct{}

// deviceFrom returns the device requireDevice authenticated.
func deviceFrom(ctx context.Context) store.Device {
	d, _ := ctx.Value(deviceKey{}).(store.Device)
	return d
}

// requireDevice authenticates the bearer token by its hash, applies the
// device's rate limit, records that it was seen and puts it in the context.
// A missing, malformed, unknown or revoked token is a 401.
func (s *Server) requireDevice(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tok, reason := bearer(r)
		if reason != "" {
			s.unauthorized(w, r, reason)
			return
		}
		sum := sha256.Sum256([]byte(tok))
		dev, err := s.d.DB.DeviceByTokenHash(ctx, sum[:])
		if errors.Is(err, store.ErrNotFound) {
			s.unauthorized(w, r, "invalid")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		devID := strconv.FormatInt(dev.ID, 10)
		if !s.devices.Allow(laneDevice, devID) {
			tooMany(w, msgTooMany)
			return
		}
		if err := s.d.DB.TouchDevice(ctx, dev.ID, s.d.Now()); err != nil {
			s.d.Log.WarnContext(ctx, "device touch failed", "component", "deviceapi", "device_id", devID,
				"outcome", "error", "error_class", errorClass(err))
		}
		next(w, r.WithContext(context.WithValue(ctx, deviceKey{}, dev)))
	}
}

// bearer returns the token of "Authorization: Bearer <token>", or the auth
// failure reason ("missing" or "invalid"). A token that is not 43 base64url
// characters is invalid without a lookup.
func bearer(r *http.Request) (string, string) {
	h := r.Header.Get("Authorization")
	if strings.TrimSpace(h) == "" {
		return "", "missing"
	}
	// RFC 7235: the auth scheme is case-insensitive.
	if len(h) <= 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", "invalid"
	}
	tok := strings.TrimSpace(h[7:])
	if !validToken(tok) {
		return "", "invalid"
	}
	return tok, ""
}

// validToken reports whether tok has the shape of a device token: 32 bytes
// as raw base64url.
func validToken(tok string) bool {
	if len(tok) != tokenLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// unauthorized counts the failure (no audit row: the endpoint is open to the
// internet) and answers 401.
func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request, reason string) {
	if m := s.d.Metrics; m != nil && m.AuthFailures != nil {
		m.AuthFailures.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("lane", string(laneDevice)), attribute.String("reason", reason)))
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, msgNotPaired)
}

func tooMany(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, msg)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, msgNotFound)
}

// internalError logs only the error's type (its text may carry values) and
// answers 500.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.serverError(w, r, errorClass(err))
}

// serverError logs a failure of class and answers 500.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, class string) {
	s.d.Log.ErrorContext(r.Context(), "device request failed", "component", "deviceapi", "http.route", r.Pattern,
		"outcome", "error", "error_class", class)
	writeError(w, http.StatusInternalServerError, msgInternal)
}

// errorClass is the error's Go type: a low-cardinality label that, unlike
// the error text, never carries values.
func errorClass(err error) string { return fmt.Sprintf("%T", err) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":"`+msgInternal+`"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
