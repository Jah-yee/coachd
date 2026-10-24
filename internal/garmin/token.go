package garmin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const tokenPath = "/personalapitoken-service/personal-api/personal-api-key/token"

// token returns a cached bearer token, exchanging the API key when needed.
// The mutex makes the exchange single-flight.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && c.now().Before(c.tokExpiry) {
		return c.tok, nil
	}
	status, hdr, body, err := c.roundTrip(ctx, c.hc, "token", http.MethodPost, c.base+tokenPath, "ApiKey "+c.key, maxJSONBody)
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "", ErrUnauthorized
	case status == http.StatusTooManyRequests:
		return "", c.rateLimited(hdr)
	case status < 200 || status > 299:
		return "", &StatusError{Endpoint: "token", Status: status}
	}
	tok, life, ok := parseTokenBody(body, c.now())
	if !ok {
		return "", &StatusError{Endpoint: "token", Status: status}
	}
	margin := min(time.Hour, life/2)
	c.tok = tok
	c.tokExpiry = c.now().Add(life - margin)
	return c.tok, nil
}

// invalidate drops the cached token if it is still the one that failed.
func (c *Client) invalidate(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok == tok {
		c.tok = ""
	}
}

const defaultTokenLife = 24 * time.Hour

// parseTokenBody extracts the bearer token and its lifetime from a token
// response. The real API returns a raw text/plain JWT; a JSON
// {access_token, expires_in} body is accepted as a fallback. It never
// returns or logs body content.
func parseTokenBody(body []byte, now time.Time) (tok string, life time.Duration, ok bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", 0, false
	}
	if body[0] == '{' {
		var tr struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
			return "", 0, false
		}
		if tr.ExpiresIn <= 0 {
			return tr.AccessToken, defaultTokenLife, true
		}
		return tr.AccessToken, time.Duration(tr.ExpiresIn) * time.Second, true
	}
	tok = string(body)
	if strings.IndexFunc(tok, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", 0, false
	}
	return tok, jwtLifetime(tok, now), true
}

// jwtLifetime reads the exp claim of a JWT without verifying the signature
// (our own token over TLS; used only for cache timing). Falls back to 24 h.
func jwtLifetime(tok string, now time.Time) time.Duration {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return defaultTokenLife
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return defaultTokenLife
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == nil {
		return defaultTokenLife
	}
	life := time.Unix(int64(*claims.Exp), 0).Sub(now)
	if life <= 0 {
		return defaultTokenLife
	}
	return life
}
