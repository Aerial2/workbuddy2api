package admin

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/requestlog"
)

func TestPricingAPIValidationAndPersistence(t *testing.T) {
	dir := t.TempDir()
	s := requestlog.New()
	s.Add(requestlog.Record{Result: "success", HasUsage: true, PromptTokens: 1000, CompletionTokens: 100})
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u", ExpiresAt: 9999999999})
	h := New(Deps{Pool: p, RequestLogs: s, APIKey: "key", PricingFile: filepath.Join(dir, "pricing.json")})
	h.pricing.mu.Lock()
	h.pricing.loaded = true
	h.pricing.p = requestlog.Pricing{}
	h.pricing.mu.Unlock()
	put := func(body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/pricing", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := put(`{}`, "wrong"); got.Code != 401 {
		t.Fatalf("auth: %d", got.Code)
	}
	if got := put(`{"input_per_m":-1}`, "key"); got.Code != 400 {
		t.Fatalf("negative: %d", got.Code)
	}
	if got := put(`{"input_per_m":2,"output_per_m":8}`, "key"); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pricing.json"))
	if err != nil || !strings.Contains(string(raw), `"input_per_m":2`) {
		t.Fatalf("persisted=%s err=%v", raw, err)
	}
	perf := h.d.RequestLogs.Performance(h.pricing.get())
	if perf.Tokens.Estimate == nil || perf.Tokens.Estimate.Amount == nil {
		t.Fatalf("estimate=%+v", perf.Tokens.Estimate)
	}
	if d := *perf.Tokens.Estimate.Amount - 0.0028; d < -1e-12 || d > 1e-12 {
		t.Fatalf("amount=%v want 0.0028", *perf.Tokens.Estimate.Amount)
	}
	perfNoPrice := h.d.RequestLogs.Performance(requestlog.Pricing{})
	if perfNoPrice.Tokens.Estimate == nil || perfNoPrice.Tokens.Estimate.Amount != nil {
		t.Fatal("no pricing must leave estimate unknown")
	}
	var saved requestlog.Pricing
	if err := json.Unmarshal(raw, &saved); err != nil || saved.InputPerMTokens == nil || *saved.InputPerMTokens != 2 {
		t.Fatalf("roundtrip=%+v", saved)
	}
}
