package models

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

const (
	DefaultProviderRequestTimeout        = 2 * time.Minute
	DefaultProviderProbeTimeout          = 60 * time.Second
	DefaultProviderTLSHandshakeTimeout   = 10 * time.Second
	DefaultProviderDialTimeout           = 30 * time.Second
	DefaultProviderResponseHeaderTimeout = 60 * time.Second
	DefaultProviderIdleConnTimeout       = 90 * time.Second
	DefaultProviderExpectContinueTimeout = 1 * time.Second
)

var defaultProviderTransport = newDefaultProviderTransport()

// NewProviderHTTPClient returns an HTTP client for model/provider traffic.
// When timeout is zero or negative, the caller is expected to enforce limits
// via context deadlines, which keeps streaming responses unbounded by the
// client's global timeout while still using the robust transport configuration.
func NewProviderHTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{Transport: defaultProviderTransport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

// newDefaultProviderTransport creates a hardened HTTP transport with aggressive
// timeout settings designed to prevent TCP-level hangs that can cause goroutine
// leaks in long-running streaming scenarios (e.g. LLM SSE streams).
//
// Key design decisions:
//   - HTTP/2 is disabled to prevent multiplexing issues where a single stuck
//     connection can block multiple requests.
//   - ResponseHeaderTimeout is set to 60s to accommodate LLM providers that may
//     take time to start generating tokens.
//   - All dial/TLS timeouts prevent connection-establishment hangs.
func newDefaultProviderTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   DefaultProviderDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   DefaultProviderTLSHandshakeTimeout,
		ResponseHeaderTimeout: DefaultProviderResponseHeaderTimeout,
		IdleConnTimeout:       DefaultProviderIdleConnTimeout,
		ExpectContinueTimeout: DefaultProviderExpectContinueTimeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		ForceAttemptHTTP2:     false,
		TLSClientConfig: &tls.Config{
			NextProtos: []string{"http/1.1"},
		},
	}
}
