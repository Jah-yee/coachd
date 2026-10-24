package oauth

import "testing"

const testSelf = "https://coachd.example.com"

func TestValidRedirectURI(t *testing.T) {
	for _, s := range []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://localhost/callback",
		"http://127.0.0.1:9/cb",
		"http://[::1]/cb",
	} {
		if !validRedirectURI(s, testSelf) {
			t.Errorf("validRedirectURI(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"http://example.com/cb",
		"cursor://cb",
		"https://u@x.com/cb",
		"https://x.com/cb#f",
		"https://x.com/cb#",
		"javascript:alert(1)",
		"https:///cb",
		"https://coachd.example.com/x",
		"https://COACHD.example.com:443/x",
		"http://localhost.evil.example/cb",
		"",
	} {
		if validRedirectURI(s, testSelf) {
			t.Errorf("validRedirectURI(%q) = true, want false", s)
		}
	}
}

func TestIsLoopbackURI(t *testing.T) {
	for _, s := range []string{"http://localhost/callback", "http://127.0.0.1:9/cb", "http://[::1]/cb"} {
		if !isLoopbackURI(s) {
			t.Errorf("isLoopbackURI(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"https://localhost/cb",
		"http://localhost.evil.example/cb",
		"http://127.0.0.1@evil.example/cb",
		"http://localhost/cb#f",
		"http://10.0.0.1/cb",
	} {
		if isLoopbackURI(s) {
			t.Errorf("isLoopbackURI(%q) = true, want false", s)
		}
	}
}

func TestMatchRedirectLoopbackPort(t *testing.T) {
	cases := []struct {
		registered, got string
		want            bool
	}{
		{"http://localhost/callback", "http://localhost:54321/callback", true},
		{"http://localhost/callback", "http://localhost/callback", true},
		{"http://localhost:3000/callback", "http://localhost:4000/callback", true},
		{"http://127.0.0.1/callback", "http://127.0.0.1:8080/callback", true},
		{"http://[::1]/cb", "http://[::1]:5/cb", true},
		{"http://localhost/callback", "http://localhost:54321/other", false},
		{"http://localhost/callback", "http://localhost:54321/callback?x=1", false},
		{"http://localhost/callback", "http://127.0.0.1:54321/callback", false},
		{"http://localhost/callback", "https://localhost:54321/callback", false},
		{"https://a.com/cb", "https://a.com/cb", true},
		{"https://a.com/cb", "https://a.com:443/cb", false},
		{"https://a.com/cb", "https://a.com/cb/", false},
		{"https://a.com/cb", "https://A.com/cb", false},
	}
	for _, c := range cases {
		if got := matchRedirect([]string{"https://other.example/x", c.registered}, c.got); got != c.want {
			t.Errorf("matchRedirect(%q, %q) = %v, want %v", c.registered, c.got, got, c.want)
		}
	}
	if matchRedirect(nil, "https://a.com/cb") {
		t.Error("matchRedirect with nothing registered = true")
	}
}
