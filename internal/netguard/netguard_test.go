package netguard

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOfflineBlocksOutsideButNotLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := Client(false)
	res, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback should work offline: %v", err)
	}
	res.Body.Close()
	for _, url := range []string{"https://huggingface.co/", "http://192.168.1.10/", "http://example.com:8080/"} {
		if _, err := c.Get(url); !errors.Is(err, ErrOffline) {
			t.Errorf("%s: want ErrOffline, got %v", url, err)
		}
	}
}

func TestOfflineIgnoresProxySettings(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	if _, err := Client(false).Get("https://huggingface.co/"); !errors.Is(err, ErrOffline) {
		t.Errorf("a proxy on loopback must not become a way out: %v", err)
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "127.0.0.1.evil.com": false, "10.0.0.1": false, "localhost.evil.com": false} {
		if IsLoopback(host) != want {
			t.Errorf("IsLoopback(%q) != %v", host, want)
		}
	}
}
