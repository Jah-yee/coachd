package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckExitCodes(t *testing.T) {
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/livez" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if code := runHealthcheck(addr); code != 0 {
		t.Fatalf("200 -> %d", code)
	}
	status = 503
	if code := runHealthcheck(addr); code != 1 {
		t.Fatalf("503 -> %d", code)
	}
	srv.Close()
	if code := runHealthcheck(addr); code != 1 {
		t.Fatalf("closed -> %d", code)
	}
}

func TestHealthcheckAddr(t *testing.T) {
	for in, want := range map[string]string{"": "127.0.0.1:9090", ":9090": "127.0.0.1:9090", ":1234": "127.0.0.1:1234", "10.0.0.1:80": "10.0.0.1:80"} {
		if got := healthAddr(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}
