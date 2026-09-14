package requestlog

import "testing"

// TestTokenEstimateMath pins the pricing formula: uncached input at the input
// rate, cached input at the cached rate, completion at the output rate.
func TestTokenEstimateMath(t *testing.T) {
	rows := []Record{{Result: "success", HasUsage: true, PromptTokens: 1000, CachedPromptTokens: 400, CompletionTokens: 100}}
	est := tokenMetrics(rows, Pricing{InputPerMTokens: ptr(2), CachedInputPerMTokens: ptr(0.5), OutputPerMTokens: ptr(8)})
	want := (600*2 + 400*0.5 + 100*8) / 1e6
	if est.Estimate == nil || est.Estimate.Amount == nil || diff(*est.Estimate.Amount, want) > 1e-12 {
		t.Fatalf("amount=%v want %v", est.Estimate, want)
	}
	noPrice := tokenMetrics(rows, Pricing{})
	if noPrice.Estimate == nil || noPrice.Estimate.Amount != nil {
		t.Fatal("missing pricing must leave amount unknown")
	}
	partial := tokenMetrics(rows, Pricing{InputPerMTokens: ptr(2)})
	// Only uncached input priced: 600*2/1e6
	if partial.Estimate.Amount == nil || diff(*partial.Estimate.Amount, 0.0012) > 1e-12 {
		t.Fatalf("partial estimate=%v", partial.Estimate.Amount)
	}
}
func TestTokenTotalsClassification(t *testing.T) {
	t1 := UsageTotals{}
	t1.add(Record{Result: "success", HasUsage: true, PromptTokens: 1000, CompletionTokens: 100, CachedPromptTokens: 400})
	t1.add(Record{Result: "success", HasUsage: true, PromptTokens: 500})
	t1.add(Record{Result: "success"})                                   // unknown usage
	t1.add(Record{Result: "failed", HasUsage: true, PromptTokens: 999}) // failures excluded
	t1.add(Record{Result: "success", HasUsage: true})                   // zero-token success
	t1.finish()
	if t1.Requests != 4 || t1.WithUsage != 3 || t1.Abnormal != 1 || t1.Prompt != 1500 || t1.CacheablePrompt != 1500 || t1.CachedPrompt != 400 || t1.ConversationRounds != 3 {
		t.Fatalf("totals=%+v", t1)
	}
	if t1.CacheHitRate == nil || diff(*t1.CacheHitRate, 400.0/1500.0*100) > 1e-9 {
		t.Fatalf("hit rate=%v", t1.CacheHitRate)
	}
	empty := UsageTotals{}
	empty.finish()
	if empty.CacheHitRate != nil {
		t.Fatal("empty cacheable set must not yield hit rate")
	}
}
func ptr(v float64) *float64 { return &v }
func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
