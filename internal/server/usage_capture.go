package server

import (
	"encoding/json"
	"net/http"

	"workbuddy2api/internal/requestlog"
)

// usageSnapshot carries per-request token accounting taken from the upstream
// usage frame only. Negative or missing values are treated as not reported.
type usageSnapshot struct {
	prompt        *int64
	completion    *int64
	cachedPrompt  *int64
	reasoning     *int64
	hasFinalUsage bool
}

func nonNeg(v *int64) *int64 {
	if v == nil || *v < 0 {
		return nil
	}
	return v
}

// captureUsage parses one SSE data payload; the final usage frame wins.
func captureUsage(s *usageSnapshot, payload []byte) {
	var chunk struct {
		Usage *struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			CachedPrompt     *int64 `json:"prompt_tokens_details_cached_tokens"`
			ReasoningTokens  *int64 `json:"completion_tokens_details_reasoning_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(payload, &chunk) != nil || chunk.Usage == nil {
		return
	}
	if u := chunk.Usage; u.PromptTokens != nil || u.CompletionTokens != nil || u.CachedPrompt != nil || u.ReasoningTokens != nil {
		s.prompt = nonNeg(u.PromptTokens)
		s.completion = nonNeg(u.CompletionTokens)
		s.cachedPrompt = nonNeg(u.CachedPrompt)
		s.reasoning = nonNeg(u.ReasoningTokens)
		s.hasFinalUsage = true
	}
}

// completionUsage extracts usage from an aggregated response; returns the
// legacy completion-token fallback (-1 when absent) plus the full snapshot.
func completionUsage(resp map[string]any) (int, *usageSnapshot) {
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return -1, &usageSnapshot{}
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return -1, &usageSnapshot{}
	}
	s := &usageSnapshot{}
	captureUsage(s, raw)
	if !s.hasFinalUsage {
		return -1, &usageSnapshot{}
	}
	toks := -1
	if s.completion != nil {
		toks = int(*s.completion)
	}
	return toks, s
}

// applyUsage copies the snapshot into the request record.
func applyUsage(r *requestlog.Record, s *usageSnapshot) {
	if s == nil || !s.hasFinalUsage {
		return
	}
	if s.prompt != nil {
		r.PromptTokens = *s.prompt
	}
	if s.cachedPrompt != nil {
		r.CachedPromptTokens = *s.cachedPrompt
	}
	if s.reasoning != nil {
		r.ReasoningTokens = *s.reasoning
	}
	if s.completion != nil {
		r.CompletionTokens = int(*s.completion)
	}
	r.HasUsage = true
}

var _ = http.StatusOK
