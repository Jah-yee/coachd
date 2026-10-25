package garmin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// DefaultBaseURL is the Garmin Connect Personal API host.
const DefaultBaseURL = "https://personal.connectapi.garmin.com"

const (
	maxJSONBody       = 32 << 20
	maxFITBody        = 64 << 20
	defaultRetryAfter = 15 * time.Minute
	// discardBody as a maxBody drains a 2xx body (up to maxDrain bytes, so
	// the connection can be reused) and returns none.
	discardBody = -1
	maxDrain    = 1 << 20
)

// Client talks to the Garmin Personal API. It is safe for concurrent use.
type Client struct {
	base    string
	key     string // guarded by mu, like the token: UseKey replaces it
	hc      *http.Client
	probeHC *http.Client // hc without following redirects, for ActivityExists
	metrics *telemetry.Metrics
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex // guards tok, tokExpiry; held across a token exchange (single-flight)
	tok       string
	tokExpiry time.Time
}

// New builds a client. A nil hc gets a 60 s timeout.
func New(baseURL, apiKey string, hc *http.Client, m *telemetry.Metrics) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	probe := *hc // same transport and timeout
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		base:    strings.TrimRight(baseURL, "/"),
		key:     apiKey,
		hc:      hc,
		probeHC: &probe,
		metrics: m,
		now:     time.Now,
		sleep:   sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	}
	return "error"
}

func (c *Client) record(ctx context.Context, endpoint string, status int, start time.Time) {
	if c.metrics == nil {
		return
	}
	c.metrics.GarminRequests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("endpoint", endpoint), attribute.String("status_class", statusClass(status))))
	c.metrics.GarminDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
		attribute.String("endpoint", endpoint)))
}

// once performs a single HTTP attempt with hc. status is 0 on transport failure.
func (c *Client) once(ctx context.Context, hc *http.Client, endpoint, method, rawURL, auth string, maxBody int64) (status int, hdr http.Header, body []byte, err error) {
	start := time.Now()
	defer func() { c.record(ctx, endpoint, status, start) }()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return 0, nil, nil, &StatusError{Endpoint: endpoint}
	}
	req.Header.Set("Authorization", auth)
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, nil, &StatusError{Endpoint: endpoint} // never wrap: url.Error embeds the URL
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		return resp.StatusCode, resp.Header, nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, resp.Header, nil, nil
	}
	if maxBody == discardBody {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		return resp.StatusCode, resp.Header, nil, nil
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return resp.StatusCode, nil, nil, &StatusError{Endpoint: endpoint}
	}
	if int64(len(body)) > maxBody {
		return resp.StatusCode, nil, nil, fmt.Errorf("garmin: %s: response too large", endpoint)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// roundTrip performs the request, retrying 5xx and transport errors up to
// three times after 1 s, 2 s and 4 s. Any other status is returned to the
// caller; an exhausted 5xx becomes a *StatusError.
func (c *Client) roundTrip(ctx context.Context, hc *http.Client, endpoint, method, rawURL, auth string, maxBody int64) (int, http.Header, []byte, error) {
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		status, hdr, body, err := c.once(ctx, hc, endpoint, method, rawURL, auth, maxBody)
		if cerr := ctx.Err(); cerr != nil {
			return 0, nil, nil, cerr
		}
		var se *StatusError
		retryable := (err == nil && status >= 500) || (errors.As(err, &se) && se.Status == 0)
		if err != nil && !retryable {
			return 0, nil, nil, err
		}
		if !retryable {
			return status, hdr, body, nil
		}
		if attempt == 3 {
			return 0, nil, nil, &StatusError{Endpoint: endpoint, Status: status}
		}
		if err := c.sleep(ctx, backoff); err != nil {
			return 0, nil, nil, err
		}
		backoff *= 2
	}
}

func (c *Client) rateLimited(h http.Header) *RateLimitedError {
	now := c.now()
	ra := strings.TrimSpace(h.Get("Retry-After"))
	if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
		return &RateLimitedError{RetryAt: now.Add(time.Duration(secs) * time.Second)}
	}
	if t, err := http.ParseTime(ra); err == nil {
		return &RateLimitedError{RetryAt: t}
	}
	return &RateLimitedError{RetryAt: now.Add(defaultRetryAfter)}
}

// get performs an authenticated GET with token handling and status mapping.
func (c *Client) get(ctx context.Context, endpoint, path string, q url.Values, maxBody int64) ([]byte, error) {
	return c.getWith(ctx, c.hc, endpoint, path, q, maxBody)
}

// getWith is get sending the data request with hc (the token exchange always
// uses c.hc). A 3xx that hc does not follow is a *StatusError.
func (c *Client) getWith(ctx context.Context, hc *http.Client, endpoint, path string, q url.Values, maxBody int64) ([]byte, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	reauthed := false
	for {
		tok, err := c.token(ctx)
		if err != nil {
			return nil, err
		}
		status, hdr, body, err := c.roundTrip(ctx, hc, endpoint, http.MethodGet, u, "Bearer "+tok, maxBody)
		if err != nil {
			return nil, err
		}
		switch {
		case status >= 200 && status <= 299:
			return body, nil
		case status == http.StatusNotFound:
			return nil, ErrNotFound
		case status == http.StatusTooManyRequests:
			return nil, c.rateLimited(hdr)
		case status == http.StatusUnauthorized && !reauthed:
			reauthed = true
			c.invalidate(tok)
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return nil, ErrUnauthorized
		}
		return nil, &StatusError{Endpoint: endpoint, Status: status}
	}
}

func dateQuery(from, to civil.Date) url.Values {
	return url.Values{"startDate": {from.String()}, "endDate": {to.String()}}
}

// decodeItems decodes a JSON array of T, keeping each element's raw JSON.
func decodeItems[T any](endpoint string, arr []byte) ([]Item[T], error) {
	if len(bytes.TrimSpace(arr)) == 0 { // 204 or empty 2xx body: no data
		return []Item[T]{}, nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(arr, &raws); err != nil {
		return nil, fmt.Errorf("garmin: %s: invalid response", endpoint)
	}
	out := make([]Item[T], 0, len(raws))
	for _, r := range raws {
		var v T
		if err := json.Unmarshal(r, &v); err != nil {
			return nil, fmt.Errorf("garmin: %s: invalid response", endpoint)
		}
		out = append(out, Item[T]{V: v, Raw: r})
	}
	return out, nil
}

// unwrap extracts the array stored under key in a JSON object.
func unwrap(endpoint string, body []byte, key string) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 { // 204 or empty 2xx body: no data
		return []byte("[]"), nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("garmin: %s: invalid response", endpoint)
	}
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return []byte("[]"), nil
	}
	return raw, nil
}

// SearchActivities lists activities between from and to (inclusive dates).
func (c *Client) SearchActivities(ctx context.Context, from, to civil.Date, start, limit int) ([]Item[ActivityDTO], error) {
	q := dateQuery(from, to)
	q.Set("start", strconv.Itoa(start))
	q.Set("limit", strconv.Itoa(limit))
	body, err := c.get(ctx, "activities_search", "/activitylist-service/activities/search/activities", q, maxJSONBody)
	if err != nil {
		return nil, err
	}
	return decodeItems[ActivityDTO]("activities_search", body)
}

// ActivityExists probes the single-activity endpoint: 200 means the activity
// exists, and only an explicit 404 from that endpoint reports it deleted
// (false, nil). Redirects are not followed, so a 404 reached through one
// cannot count. Every other outcome is an error: 401/403 ErrUnauthorized, 429
// *RateLimitedError, an exhausted 5xx or transport failure, a 3xx and any
// other status *StatusError. The response body is drained and discarded.
func (c *Client) ActivityExists(ctx context.Context, id int64) (bool, error) {
	_, err := c.getWith(ctx, c.probeHC, "activity", "/activity-service/activity/"+strconv.FormatInt(id, 10), nil, discardBody)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	}
	return false, err
}

// DownloadActivityFIT downloads the original FIT file. Garmin may return it
// zipped; the first *.fit entry is returned in that case.
func (c *Client) DownloadActivityFIT(ctx context.Context, id int64) ([]byte, error) {
	body, err := c.get(ctx, "activity_file", "/download-service/files/activity/"+strconv.FormatInt(id, 10), nil, maxFITBody)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(body, []byte("PK")) {
		return body, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, errors.New("garmin: activity_file: invalid zip")
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".fit") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errors.New("garmin: activity_file: invalid zip")
		}
		defer rc.Close()
		out, err := io.ReadAll(io.LimitReader(rc, maxFITBody+1))
		if err != nil {
			return nil, errors.New("garmin: activity_file: invalid zip")
		}
		if int64(len(out)) > maxFITBody {
			return nil, errors.New("garmin: activity_file: fit entry too large")
		}
		return out, nil
	}
	return nil, errors.New("garmin: activity_file: no .fit entry in zip")
}

// SleepRange returns daily sleep summaries.
func (c *Client) SleepRange(ctx context.Context, from, to civil.Date) ([]Item[SleepDTO], error) {
	body, err := c.get(ctx, "sleep", "/sleep-service/sleep/dailySleepSummariesByDate", dateQuery(from, to), maxJSONBody)
	if err != nil {
		return nil, err
	}
	return decodeItems[SleepDTO]("sleep", body)
}

// HRVRange returns daily HRV summaries.
func (c *Client) HRVRange(ctx context.Context, from, to civil.Date) ([]Item[HRVSummaryDTO], error) {
	body, err := c.get(ctx, "hrv", "/hrv-service/hrv/daily/"+from.String()+"/"+to.String(), nil, maxJSONBody)
	if err != nil {
		return nil, err
	}
	arr, err := unwrap("hrv", body, "hrvSummaries")
	if err != nil {
		return nil, err
	}
	return decodeItems[HRVSummaryDTO]("hrv", arr)
}

// DailySummariesV2 returns v2 daily summaries.
func (c *Client) DailySummariesV2(ctx context.Context, from, to civil.Date) ([]Item[DailySummaryV2DTO], error) {
	body, err := c.get(ctx, "daily_v2", "/usersummary-service/v2/dailysummaries", dateQuery(from, to), maxJSONBody)
	if err != nil {
		return nil, err
	}
	arr, err := unwrap("daily_v2", body, "data")
	if err != nil {
		return nil, err
	}
	return decodeItems[DailySummaryV2DTO]("daily_v2", arr)
}

// WeightRange returns daily weight summaries. weight/all returns 400 on the
// live API; weight/range works.
func (c *Client) WeightRange(ctx context.Context, from, to civil.Date) ([]Item[WeightDTO], error) {
	body, err := c.get(ctx, "weight", "/weight-service/weight/range/"+from.String()+"/"+to.String(), nil, maxJSONBody)
	if err != nil {
		return nil, err
	}
	arr, err := unwrap("weight", body, "dailyWeightSummaries")
	if err != nil {
		return nil, err
	}
	return decodeItems[WeightDTO]("weight", arr)
}
