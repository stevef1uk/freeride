package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readCostLedger(t *testing.T, path string) map[string]int64 {
	t.Helper()
	data, err := os.ReadFile(path + ".usd")
	if os.IsNotExist(err) {
		return map[string]int64{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var ledger map[string]int64
	if err = json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func trackedTestCost(t *testing.T, path, workflow string) *workflowCostAccounting {
	t.Helper()
	_, accounting := trackOpenRouterWorkflowCost("rig", workflow, path, []byte(`{"model":"sample","messages":[{"role":"user","content":"hello"}]}`), nil)
	if accounting == nil {
		t.Fatal("missing accounting")
	}
	return accounting
}

func TestCostAccountingIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	for i := 0; i < 30; i++ {
		r := trackedTestCost(t, path, "wf")
		r.accountResponse([]byte(`{"usage":{"cost":0.00001}}`))
		r.accountResponse([]byte(`{"usage":{"cost":0.00001}}`))
	}
	ledger := readCostLedger(t, path)
	for _, key := range []string{budgetKey("rig", "wf"), budgetKey("rig")} {
		if ledger[key] != 300 {
			t.Fatalf("%s: got %d, want actual cost 300 microdollars", key, ledger[key])
		}
	}
}

func TestUnknownChargesAreNotRecordedAsFree(t *testing.T) {
	for _, body := range []string{`{}`, `{"usage":{}}`, `{"usage":{"cost":null}}`, `{"usage":{"cost":-1}}`, `{"usage":{"cost":"0"}}`, `broken`} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "budget.json")
			r := trackedTestCost(t, path, "wf")
			r.accountResponse([]byte(body))
			if got := readCostLedger(t, path)[budgetKey("rig", "wf")]; got != 0 {
				t.Fatalf("unknown charge recorded: %d", got)
			}
			r.finishAccounting()
			coverage, err := os.ReadFile(path + ".usd.coverage")
			if err != nil || !strings.Contains(string(coverage), "unknown") {
				t.Fatalf("coverage=%s err=%v", coverage, err)
			}
		})
	}
}

func TestCostAccountingConcurrentWorkflows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	first := trackedTestCost(t, path, "wf1")
	second := trackedTestCost(t, path, "wf2")
	var wg sync.WaitGroup
	for _, r := range []*workflowCostAccounting{first, second} {
		wg.Add(1)
		go func(r *workflowCostAccounting) {
			defer wg.Done()
			if err := r.recordCost(.00002); err != nil {
				t.Error(err)
			}
		}(r)
	}
	wg.Wait()
	ledger := readCostLedger(t, path)
	if ledger[budgetKey("rig")] != 40 || ledger[budgetKey("rig", "wf1")] != 20 || ledger[budgetKey("rig", "wf2")] != 20 {
		t.Fatal(ledger)
	}
}

func TestStreamingCostAccountingPreservesResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	r := trackedTestCost(t, path, "wf")
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"usage\":{\"cost\":0.00003}}\r\n\ndata: [DONE]\n\n"
	body := &usageAccountingBody{ReadCloser: io.NopCloser(strings.NewReader(stream)), accounting: r}
	var got bytes.Buffer
	buf := make([]byte, 7)
	for {
		n, err := body.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.String() != stream {
		t.Fatal("stream changed")
	}
	if cost := readCostLedger(t, path)[budgetKey("rig", "wf")]; cost != 30 {
		t.Fatalf("cost %d", cost)
	}
}

type budgetTestTransport func(*http.Request) (*http.Response, error)

func (f budgetTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyAccountsProviderCostBeforeReturning(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		stream     bool
		want       int64
	}{
		{"nonstream", `{"model":"sample","choices":[{"message":{"content":"ok"}}],"usage":{"cost":0.00001}}`, 200, false, 10},
		{"stream", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"usage\":{\"cost\":0.00002}}\n\ndata: [DONE]\n\n", 200, true, 20},
		{"rejected", `{"error":{"message":"rate limited"}}`, 429, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "budget.json")
			t.Setenv("FREERIDE_EXECUTION_BUDGET_PATH", path)
			setupIsolatedLocalProxy(t, "https://openrouter.ai/api", "local/budget-accounting")
			previous := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = previous })
			http.DefaultClient = &http.Client{Transport: budgetTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "openrouter.ai" {
					t.Fatalf("unexpected upstream: %s", req.URL)
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			payload := fmt.Sprintf(`{"model":"local/budget-accounting","messages":[{"role":"user","content":"hello"}],"max_tokens":100,"stream":%t}`, tc.stream)
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
			req.Header.Set("X-GasTown-Rig", "rig")
			req.Header.Set("X-GasTown-Workflow", "wf")
			rec := httptest.NewRecorder()
			handleChatCompletions(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			ledger := readCostLedger(t, path)
			if got := ledger[budgetKey("rig", "wf")]; got != tc.want {
				t.Fatalf("accounted %d, want %d", got, tc.want)
			}
		})
	}
}
