// Package netguard is the one way USB LLMS reaches the network. In offline mode it can only dial loopback.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ErrOffline is returned for any outside connection while offline.
var ErrOffline = errors.New("offline mode blocks this; run `usb-llms mode online` first")

// IsLoopback accepts only names that cannot leave the machine.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Client returns an HTTP client for the given mode. It has no overall timeout because model downloads run for hours.
func Client(online bool) *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		ResponseHeaderTimeout: 60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ForceAttemptHTTP2:     true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if !online && !IsLoopback(host) {
				return nil, fmt.Errorf("%s: %w", host, ErrOffline)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	if online {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: transport}
}

// Local talks to services on this machine. Engine calls can stream for a long time, so it sets no timeouts of its own.
func Local() *http.Client {
	c := Client(false)
	c.Transport.(*http.Transport).ResponseHeaderTimeout = 0
	return c
}
