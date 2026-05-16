package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// liveProxyIntegrationEnabled is true when tests may call a running Freeride on FREERIDE_TEST_URL.
func liveProxyIntegrationEnabled() bool {
	return os.Getenv("FREERIDE_INTEGRATION") == "1"
}

func skipUnlessLiveProxy(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping live proxy test under -short")
	}
	if !liveProxyIntegrationEnabled() {
		t.Skip("set FREERIDE_INTEGRATION=1 to run live proxy tests against FREERIDE_TEST_URL")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(proxyURL + "/api/tags")
	if err != nil {
		t.Skipf("live proxy not reachable at %s: %v", proxyURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("live proxy at %s returned %d", proxyURL, resp.StatusCode)
	}
}

// skipOnLiveProxyOverload skips when the running proxy cannot serve (cooldown, rate limits, etc.).
func skipOnLiveProxyOverload(t *testing.T, status int, body []byte) {
	t.Helper()
	if status == http.StatusOK {
		return
	}
	msg := string(body)
	switch status {
	case http.StatusServiceUnavailable:
		if strings.Contains(msg, "cooldown") ||
			strings.Contains(msg, "overloaded") ||
			strings.Contains(msg, "No models available") {
			t.Skipf("live proxy overloaded: %s", msg)
		}
	case http.StatusTooManyRequests:
		t.Skipf("live proxy rate limited: %s", msg)
	case http.StatusNotFound:
		t.Skipf("live proxy returned 404: %s", msg)
	}
}

func resetProxyCooldownsForTest() {
	cooldownMu.Lock()
	cooldowns = make(map[string]*cooldownEntry)
	cooldownMu.Unlock()
}
