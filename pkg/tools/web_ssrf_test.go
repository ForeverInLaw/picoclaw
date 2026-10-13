package tools

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebFetchBenchmarkNetworkPolicy(t *testing.T) {
	cases := []struct {
		name      string
		host      string
		whitelist []string
		disabled  bool
		blocked   bool
	}{
		{name: "lower boundary", host: "198.18.0.0", blocked: true},
		{name: "upper boundary", host: "198.19.255.255", blocked: true},
		{name: "adjacent lower", host: "198.17.255.255"},
		{name: "adjacent upper", host: "198.20.0.0"},
		{name: "IPv4 mapped IPv6", host: "[::ffff:198.18.0.0]", blocked: true},
		{name: "exact whitelist", host: "198.18.0.0", whitelist: []string{"198.18.0.0"}},
		{name: "CIDR whitelist", host: "198.19.255.255", whitelist: []string{"198.18.0.0/15"}},
		{name: "disabled guard", host: "198.18.0.0", disabled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disabled {
				withPrivateWebFetchHostsAllowed(t)
			}
			var requests atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("fetched through fixture proxy"))
			}))
			defer proxy.Close()
			proxyHost, _ := serverHostAndPort(t, proxy.URL)
			whitelist := append([]string{proxyHost}, tc.whitelist...)
			tool, err := NewWebFetchToolWithConfig(50000, proxy.URL, format, testFetchLimit, whitelist)
			require.NoError(t, err)
			result := tool.Execute(t.Context(), map[string]any{"url": "http://" + tc.host + "/fixture"})
			if tc.blocked {
				require.True(t, result.IsError)
				require.Contains(t, result.ForLLM, "private or local network")
				require.Zero(t, requests.Load(), "blocked URLs must not reach the HTTP proxy")
				return
			}
			require.False(t, result.IsError, result.ForLLM)
			require.Contains(t, result.ForLLM, "fetched through fixture proxy")
			require.Equal(t, int32(1), requests.Load())
		})
	}
}

func TestWebFetchBenchmarkRedirectPolicy(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "whitelisted"}[allowed], func(t *testing.T) {
			var requests atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "http://198.19.255.255/target", http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("whitelisted redirected content"))
			}))
			defer proxy.Close()
			proxyHost, _ := serverHostAndPort(t, proxy.URL)
			whitelist := []string{proxyHost}
			if allowed {
				whitelist = append(whitelist, "198.18.0.0/15")
			}
			tool, err := NewWebFetchToolWithConfig(50000, proxy.URL, format, testFetchLimit, whitelist)
			require.NoError(t, err)
			result := tool.Execute(t.Context(), map[string]any{"url": "http://198.20.0.0/start"})
			if !allowed {
				require.True(t, result.IsError)
				require.Contains(t, result.ForLLM, "redirect target is private or local network")
				require.Equal(t, int32(1), requests.Load(), "blocked redirect must stop after the initial response")
				return
			}
			require.False(t, result.IsError, result.ForLLM)
			require.Contains(t, result.ForLLM, "whitelisted redirected content")
			require.Equal(t, int32(2), requests.Load())
		})
	}
}
