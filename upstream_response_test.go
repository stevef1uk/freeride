package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpstreamRequestTimeout(t *testing.T) {
	setupIsolatedLocalProxy(t, "http://127.0.0.1:1", "local/timeout-test")
	for _, tt := range []struct {
		name, model, value string
		want               time.Duration
	}{
		{"cloud reasoning default", "openai/gpt-5.6-luna", "", 4 * time.Minute},
		{"local default", "local/timeout-test", "", 20 * time.Minute},
		{"cloud override", "openai/gpt-5.6-luna", "90s", 90 * time.Second},
		{"local override", "local/timeout-test", "5m", 5 * time.Minute},
		{"invalid", "openai/gpt-5.6-luna", "bad", 4 * time.Minute},
		{"zero", "openai/gpt-5.6-luna", "0s", 4 * time.Minute},
		{"negative", "openai/gpt-5.6-luna", "-1s", 4 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FREERIDE_UPSTREAM_TIMEOUT", tt.value)
			if got := upstreamRequestTimeout(tt.model); got != tt.want {
				t.Fatalf("timeout = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNonStreamingBodyFailureFallsBack(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			var brokenCalls, fallbackCalls int
			broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				brokenCalls++
				w.Header().Set("X-Broken-Upstream", "must-not-leak")
				w.Header().Set("Content-Type", "application/json")
				// Headers are successful, but the response is cut off mid-body.
				w.Header().Set("Content-Length", "1000")
				_, _ = io.WriteString(w, `{"choices":[`)
			}))
			defer broken.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls++
				w.Header().Set("Content-Type", "application/json")
				// Exercise gzip decoding before the protocol translators too.
				w.Header().Set("Content-Encoding", "gzip")
				gz := gzip.NewWriter(w)
				_, _ = io.WriteString(gz, `{"choices":[{"message":{"role":"assistant","content":"fallback answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
				_ = gz.Close()
			}))
			defer fallback.Close()
			setupIsolatedLocalProxy(t, broken.URL, "local/broken-body")
			configMutex.Lock()
			globalModelsConfig.LocalOpenAI = append(globalModelsConfig.LocalOpenAI, localOpenAIModel{
				ID: "local/fallback-body", Endpoint: fallback.URL, Model: "fallback-model",
			})
			configMutex.Unlock()

			payload := `{"model":"local/broken-body","stream":false,"messages":[{"role":"user","content":"hello"}]}`
			rec := httptest.NewRecorder()
			handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload)))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fallback answer") {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			if brokenCalls != 1 || fallbackCalls != 1 {
				t.Fatalf("calls: broken=%d fallback=%d, want 1 each", brokenCalls, fallbackCalls)
			}
			if rec.Header().Get("X-Broken-Upstream") != "" || rec.Header().Get("Content-Encoding") != "" {
				t.Fatalf("stale upstream headers leaked: %v", rec.Header())
			}
			if !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("invalid translated JSON: %s", rec.Body.String())
			}
		})
	}
}

func TestNonStreamingBodyFailureReturnsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Header().Set("X-Broken-Upstream", "must-not-leak")
		_, _ = io.WriteString(w, `{"choices":[`)
	}))
	defer upstream.Close()
	setupIsolatedLocalProxy(t, upstream.URL, "local/only-broken-body")
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"local/only-broken-body","messages":[{"role":"user","content":"hello"}]}`)))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Failed to read upstream body") {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Broken-Upstream") != "" {
		t.Fatal("failed upstream headers leaked")
	}
}

func TestNonStreamingBodyDeadlineFallsBack(t *testing.T) {
	t.Setenv("FREERIDE_UPSTREAM_TIMEOUT", "100ms")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/slow/v1/chat/completions" {
			// Reproduce providers that acknowledge immediately, then stall while
			// generating. The deadline must cover the body, not only headers.
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"deadline fallback"}}]}`)
	}))
	defer upstream.Close()
	setupIsolatedLocalProxy(t, upstream.URL+"/slow", "local/slow-body")
	configMutex.Lock()
	globalModelsConfig.LocalOpenAI = append(globalModelsConfig.LocalOpenAI, localOpenAIModel{
		ID: "local/fast-body", Endpoint: upstream.URL, Model: "fast-model",
	})
	configMutex.Unlock()
	rec := httptest.NewRecorder()
	handleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"local/slow-body","messages":[{"role":"user","content":"hello"}]}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "deadline fallback") {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

type trackedResponseBody struct {
	io.Reader
	closed bool
}

func (b *trackedResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestReadUpstreamBodyClosesOnGzipErrors(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(gz, "answer")
	_ = gz.Close()
	for _, data := range [][]byte{[]byte("not gzip"), compressed.Bytes()[:compressed.Len()-4]} {
		body := &trackedResponseBody{Reader: bytes.NewReader(data)}
		resp := &http.Response{Body: body, Header: http.Header{"Content-Encoding": {"gzip"}}}
		if _, err := readUpstreamBody(resp); err == nil {
			t.Fatal("expected malformed/truncated gzip error")
		}
		if !body.closed {
			t.Fatal("upstream body was not closed on error")
		}
	}
}
