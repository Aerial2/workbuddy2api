package admin

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sync"

	"workbuddy2api/internal/requestlog"
)

// pricingState keeps the per-Mtoken price overrides in memory, mirrored to a
// small JSON file so they survive restarts. No request text is ever stored.
type pricingState struct {
	mu     sync.Mutex
	p      requestlog.Pricing
	loaded bool
	file   string
}

func (s *pricingState) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true
	if s.file == "" {
		return
	}
	raw, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, &s.p)
}
func (s *pricingState) get() requestlog.Pricing {
	s.load()
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := s.p
	return copied
}
func (s *pricingState) set(p requestlog.Pricing) requestlog.Pricing {
	s.load()
	s.mu.Lock()
	s.p = p
	raw, err := json.Marshal(p)
	if err == nil && s.file != "" {
		_ = os.WriteFile(s.file, append(raw, '\n'), 0o600)
	}
	s.mu.Unlock()
	return p
}
func validPrice(v *float64) bool {
	return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0)
}
func nonNeg(v *float64) *float64 {
	if v != nil && *v < 0 {
		return nil
	}
	return v
}

func (h *Handler) getPricing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, h.pricing.get())
}
func (h *Handler) putPricing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	body, err := readLimited(r, 16<<10)
	if err != nil {
		writeErr(w, 400, "invalid_request", err.Error())
		return
	}
	var p requestlog.Pricing
	if err := json.Unmarshal(body, &p); err != nil {
		writeErr(w, 400, "invalid_json", "价格配置不是合法 JSON")
		return
	}
	if !validPrice(p.InputPerMTokens) || !validPrice(p.CachedInputPerMTokens) || !validPrice(p.OutputPerMTokens) {
		writeErr(w, 400, "invalid_pricing", "单价必须为非负数字，留空表示不估算")
		return
	}
	p.InputPerMTokens = nonNeg(p.InputPerMTokens)
	p.CachedInputPerMTokens = nonNeg(p.CachedInputPerMTokens)
	p.OutputPerMTokens = nonNeg(p.OutputPerMTokens)
	writeJSON(w, 200, h.pricing.set(p))
}
