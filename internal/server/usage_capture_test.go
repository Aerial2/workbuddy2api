package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/requestlog"
)

func sseUsage(usage string) string {
	return "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"x\"}}],\"usage\":" + usage + "}\n\ndata: [DONE]\n\n"
}

func TestTokenCaptureFromUsageFrames(t *testing.T) {
	for _, tc := range []struct {
		name, usage                           string
		hasUsage                              bool
		prompt, cached, completion, reasoning int64
	}{
		{name: "full details", usage: `{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details_cached_tokens":800,"completion_tokens_details_reasoning_tokens":20}`, hasUsage: true, prompt: 1000, cached: 800, completion: 50, reasoning: 20},
		{name: "no details", usage: `{"prompt_tokens":100,"completion_tokens":10}`, hasUsage: true, prompt: 100, completion: 10},
		{name: "negative ignored", usage: `{"prompt_tokens":-5,"completion_tokens":-3}`, hasUsage: true},
		{name: "invalid usage", usage: `null`, hasUsage: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := requestlog.New()
			up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseUsage(tc.usage), true })
			h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u", ExpiresAt: 9999999999}), Upstream: up, RequestLogs: s})
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[],"stream":true}`)))
			got := s.Query(requestlog.Filter{}).Data[0]
			if tc.hasUsage {
				if !got.HasUsage || got.PromptTokens != tc.prompt || got.CachedPromptTokens != tc.cached || got.ReasoningTokens != tc.reasoning {
					t.Fatalf("record=%+v", got)
				}
			} else {
				if got.PromptTokens != 0 || got.CachedPromptTokens != 0 || got.ReasoningTokens != 0 {
					t.Fatalf("negative or invalid usage leaked: %+v", got)
				}
			}
			if tc.completion > 0 && got.CompletionTokens != int(tc.completion) {
				t.Fatalf("completion=%d", got.CompletionTokens)
			}
		})
	}
	s := requestlog.New()
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseUsage(`{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details_cached_tokens":4}`), true
	})
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u", ExpiresAt: 9999999999}), Upstream: up, RequestLogs: s})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	got := s.Query(requestlog.Filter{}).Data[0]
	if got.PromptTokens != 10 || got.CachedPromptTokens != 4 || got.CompletionTokens != 2 || !got.HasUsage {
		t.Fatalf("sync record=%+v", got)
	}
}
