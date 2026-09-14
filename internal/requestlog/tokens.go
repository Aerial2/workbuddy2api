package requestlog

// UsageTotals aggregates token accounting. Non-usage requests are tracked
// separately so the UI can distinguish "no data" from zero.
type UsageTotals struct {
	Requests    int   `json:"requests"`
	WithUsage   int   `json:"with_usage"`
	Prompt      int64 `json:"prompt"`
	Completion  int64 `json:"completion"`
	Reasoning   int64 `json:"reasoning"`
	TotalTokens int64 `json:"total_tokens"`
	// CacheablePrompt includes every usage-bearing request with prompt tokens;
	// upstream not reporting a cache breakdown counts as a zero cached read.
	CacheablePrompt int64    `json:"cacheable_prompt"`
	CachedPrompt    int64    `json:"cached_prompt"`
	CacheHitRate    *float64 `json:"cache_hit_rate"`
	// ConversationRounds counts successful requests; retries are not rounds.
	ConversationRounds int `json:"conversation_rounds"`
	Abnormal           int `json:"abnormal"`
}

func (t *UsageTotals) add(r Record) {
	if r.Result != "success" {
		return
	}
	t.Requests++
	// Upstream usage frame absent → tokens unknown, excluded from every total.
	if !r.HasUsage {
		t.Abnormal++
		return
	}
	t.WithUsage++
	t.ConversationRounds++
	t.Prompt += r.PromptTokens
	t.Completion += int64(max(r.CompletionTokens, 0))
	t.Reasoning += r.ReasoningTokens
	t.TotalTokens = t.Prompt + t.Completion
	// prompt_tokens_details absence is indistinguishable from a zero cache read;
	// only requests that reported positive prompt tokens count toward the rate.
	if r.PromptTokens > 0 {
		t.CacheablePrompt += r.PromptTokens
		t.CachedPrompt += min64(r.CachedPromptTokens, r.PromptTokens)
	}
}
func min64(a, b int64) int64 {
	if b < a {
		return b
	}
	return a
}
func (t *UsageTotals) finish() {
	if t.CacheablePrompt > 0 {
		v := 100 * float64(t.CachedPrompt) / float64(t.CacheablePrompt)
		t.CacheHitRate = &v
	}
}

// Pricing is user-configured; missing prices leave the estimate nil.
type Pricing struct {
	InputPerMTokens       *float64 `json:"input_per_m"`
	CachedInputPerMTokens *float64 `json:"cached_input_per_m"`
	OutputPerMTokens      *float64 `json:"output_per_m"`
}
type UsageEstimate struct {
	Pricing Pricing  `json:"pricing"`
	Amount  *float64 `json:"amount"`
	// KnownTurns mirrors WithUsage: requests without usage are excluded.
	KnownTurns   int `json:"known_turns"`
	UnknownTurns int `json:"unknown_turns"`
}

func estimate(t UsageTotals, p Pricing) *UsageEstimate {
	if p.InputPerMTokens == nil && p.CachedInputPerMTokens == nil && p.OutputPerMTokens == nil {
		return &UsageEstimate{Pricing: Pricing{}, KnownTurns: t.WithUsage, UnknownTurns: t.Abnormal}
	}
	amount := 0.0
	uncached := t.Prompt - t.CachedPrompt
	if p.InputPerMTokens != nil {
		amount += *p.InputPerMTokens * float64(uncached) / 1e6
	}
	if p.CachedInputPerMTokens != nil {
		amount += *p.CachedInputPerMTokens * float64(t.CachedPrompt) / 1e6
	}
	if p.OutputPerMTokens != nil {
		amount += *p.OutputPerMTokens * float64(t.Completion) / 1e6
	}
	return &UsageEstimate{Pricing: Pricing{InputPerMTokens: copyPtr(p.InputPerMTokens), CachedInputPerMTokens: copyPtr(p.CachedInputPerMTokens), OutputPerMTokens: copyPtr(p.OutputPerMTokens)}, Amount: &amount, KnownTurns: t.WithUsage, UnknownTurns: t.Abnormal}
}

func copyPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

type TokenMetrics struct {
	UsageTotals
	Estimate *UsageEstimate `json:"estimate"`
}

func tokenMetrics(rows []Record, p Pricing) TokenMetrics {
	t := UsageTotals{}
	for _, r := range rows {
		t.add(r)
	}
	t.finish()
	return TokenMetrics{UsageTotals: t, Estimate: estimate(t, p)}
}
