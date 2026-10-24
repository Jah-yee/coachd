package deviceapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stefiix92/coachd/internal/store"
)

func TestDeviceRequestsMetric(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := ts.pair(t) // one pairing_create, two pairing_poll
	ts.do(t, "GET", "/device/v1/workouts", "bad", "")
	ts.do(t, "GET", "/device/v1/workouts", tok, "")
	ts.do(t, "GET", "/device/v1/nope", "", "")
	// Fetched, then withdrawn: the ack finds a delivery row but the wrong status.
	p := ts.addProposal(t, "run", ts.today(), store.ProposalApproved)
	if rec := ts.do(t, "GET", "/device/v1/workouts/"+p.ID+"/fit", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("fit: status %d", rec.Code)
	}
	ts.withdraw(t, p.ID)
	if rec := ts.do(t, "POST", "/device/v1/workouts/"+p.ID+"/ack", tok, ""); rec.Code != http.StatusConflict {
		t.Fatalf("ack of withdrawn workout: status %d", rec.Code)
	}
	// The global create bucket holds pairCreateBurst; pair() used one.
	for i := 0; i < pairCreateBurst+1; i++ {
		ts.do(t, "POST", "/device/v1/pairings", "", "")
	}
	got := ts.counter(t, "coachd.device.requests", "endpoint", "outcome")
	for _, k := range []string{"workouts/unauthorized", "workouts/ok", "unknown/not_found", "workout_ack/conflict",
		"pairing_create/rate_limited", "pairing_create/ok", "pairing_poll/ok"} {
		if got[k] == 0 {
			t.Errorf("%s not counted: %v", k, got)
		}
	}
}

func TestDeviceRequestsMetricNoPathLabels(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := ts.pair(t)
	ts.do(t, "GET", "/device/v1/nope/123", "", "")
	ts.do(t, "GET", "/device/v1/workouts/abc/fit", tok, "")
	ts.do(t, "GET", "/device/v1/pairings/xyz", "", "")
	for k := range ts.counter(t, "coachd.device.requests", "endpoint", "outcome") {
		if strings.Contains(k, "123") || strings.Contains(k, "abc") || strings.Contains(k, "xyz") || strings.Count(k, "/") != 1 {
			t.Errorf("label %q carries a path value", k)
		}
	}
	// A single series per endpoint/outcome: no attribute value has a slash.
	for _, key := range []string{"endpoint", "outcome"} {
		for k := range ts.counter(t, "coachd.device.requests", key) {
			if strings.Contains(k, "/") {
				t.Errorf("%s value %q contains /", key, k)
			}
		}
	}
}

func TestDeviceRequestsNilMetrics(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.d.Metrics = nil
	ts.h = ts.srv.Handler()
	if rec := ts.do(t, "GET", "/device/v1/workouts", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}
