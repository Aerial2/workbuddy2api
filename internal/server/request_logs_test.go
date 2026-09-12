package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/requestlog"
)

func TestRequestLogsCapture(t *testing.T) {
	for _, tc := range []struct {
		name, upbody      string
		stream            bool
		code              int
		result, errorCode string
	}{
		{"sync", sseOK, false, 200, "success", ""},
		{"stream", sseOK, true, 200, "success", ""},
		{"empty stream", "", true, 200, "failed", "stream_error"},
		{"empty sync", "", false, 502, "failed", "upstream_parse"},
		{"malformed stream", "data: invalid\n\n" + sseOK, true, 200, "failed", "upstream_parse"},
		{"error event", "data: {\"error\":{\"message\":\"private reply\"}}\n\ndata: [DONE]\n\n", true, 200, "failed", "upstream_event_error"},
		{"error event sync", "data: {\"error\":{\"message\":\"private reply\"}}\n\ndata: [DONE]\n\n", false, 502, "failed", "upstream_event_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := requestlog.New()
			up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, tc.upbody, true })
			h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "private token", ExpiresAt: 9999999999}), Upstream: up, RequestLogs: s})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"glm-5.2","messages":[{"role":"user","content":"private prompt"}],"stream":%t}`, tc.stream))))
			p := s.Query(requestlog.Filter{})
			if len(p.Data) != 1 {
				t.Fatalf("rows=%d", len(p.Data))
			}
			r := p.Data[0]
			if w.Code != tc.code || r.Status != tc.code || r.Result != tc.result || r.ErrorCode != tc.errorCode || r.UID != "u1" || r.Attempts != 1 || r.Retries != 0 || r.FinishedAt.Before(r.StartedAt) {
				t.Fatalf("HTTP=%d record=%+v", w.Code, r)
			}
			raw, _ := json.Marshal(p)
			if strings.Contains(string(raw), "private") {
				t.Fatalf("private data stored: %s", raw)
			}
		})
	}
}

func TestRequestLogsEarlyExitAndRetry(t *testing.T) {
	s := requestlog.New()
	h := NewHandler(Config{Pool: testPoolWith(), APIKey: "key", MaxBodyBytes: 10, RequestLogs: s})
	for _, tc := range []struct{ body, key, code string }{
		{"", "", "invalid_api_key"}, {strings.Repeat("x", 11), "key", "request_body_too_large"}, {"{}", "key", "no_healthy_account"},
	} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.key)
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got := s.Query(requestlog.Filter{}).Data[0]; got.ErrorCode != tc.code || got.Result != "failed" || got.Attempts != 0 {
			t.Fatalf("early exit=%+v", got)
		}
	}
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		if calls == 1 {
			return 429, "rate limit private reply", false
		}
		return 200, sseOK, true
	})
	h = NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "a", ExpiresAt: 9999999999}, &auth.Auth{UID: "u2", AccessToken: "b", ExpiresAt: 9999999999}), Upstream: up, RequestLogs: s})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	got := s.Query(requestlog.Filter{}).Data[0]
	if len(got.AttemptDetails) != 2 || got.AttemptDetails[0].ErrorCode != "soft_rate" || got.AttemptDetails[1].ErrorCode != "" {
		t.Fatalf("attempt outcomes=%+v", got.AttemptDetails)
	}
	if got.Result != "success" || got.Retries != 1 || got.Attempts != 2 || len(got.Accounts) != 2 || got.LastFailure != requestlog.Describe("soft_rate") {
		t.Fatalf("retry=%+v", got)
	}
}

func TestRequestLogsTimeoutAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		status                 int
		body, want             string
		transport, readTimeout bool
	}{
		{name: "rate limit", status: 429, body: "rate limit private", want: "soft_rate"},
		{name: "expired login", status: 401, body: "12153 offline session", want: "session_dead"},
		{name: "transport", transport: true, want: "timeout"},
		{name: "read timeout", readTimeout: true, want: "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := requestlog.New()
			up := newFakeUpstream(t, func(string) (int, string, bool) { return tc.status, tc.body, false })
			if tc.transport || tc.readTimeout {
				up.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
					if tc.transport {
						return nil, context.DeadlineExceeded
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(&bytesAndError{})}, nil
				})
			}
			h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u", AccessToken: "a", ExpiresAt: 9999999999}), Upstream: up, RequestLogs: s})
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[],"stream":true}`)))
			got := s.Query(requestlog.Filter{}).Data[0]
			if tc.readTimeout {
				if got.Status != 200 || got.ErrorCode != "timeout" {
					t.Fatalf("stream timeout=%+v", got)
				}
			} else if got.Status != 503 || got.LastFailure != requestlog.Describe(tc.want) {
				t.Fatalf("failure=%+v", got)
			}
			if got.Result != "failed" {
				t.Fatal("failure reported as success")
			}
		})
	}
}

func TestRequestLogsOnlyCompleted(t *testing.T) {
	s := requestlog.New()
	h := NewHandler(Config{RequestLogs: s})
	h.captureRequest(func(w http.ResponseWriter, r *http.Request) {
		if s.Query(requestlog.Filter{}).Retained != 0 {
			t.Fatal("unfinished request logged")
		}
		w.WriteHeader(200)
	})(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil))
	if s.Query(requestlog.Filter{}).Retained != 1 {
		t.Fatal("completed request missing")
	}
}

type bytesAndError struct{ done bool }

func (r *bytesAndError) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, []byte("data: {}")), context.DeadlineExceeded
}

func TestChatStatsReaderPreservesReadError(t *testing.T) {
	r := newChatStatsReaderSince(&bytesAndError{}, time.Now())
	raw, err := io.ReadAll(r)
	if string(raw) != "data: {}" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	if requestErrorCode(err, "stream_error") != "timeout" {
		t.Fatal("timeout misclassified")
	}
}

type failedLogWriter struct{ http.ResponseWriter }

func (w failedLogWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRequestLogsDisconnectedAndPanic(t *testing.T) {
	s := requestlog.New()
	h := NewHandler(Config{RequestLogs: s})
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	h.captureRequest(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) })(failedLogWriter{httptest.NewRecorder()}, r)
	if got := s.Query(requestlog.Filter{}).Data[0]; got.Status != 200 || got.ErrorCode != "cancelled" {
		t.Fatalf("disconnect=%+v", got)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		h.captureRequest(func(http.ResponseWriter, *http.Request) { panic("private panic") })(httptest.NewRecorder(), r)
	}()
	if got := s.Query(requestlog.Filter{}).Data[0]; got.ErrorCode != "internal_error" || got.Result != "failed" {
		t.Fatalf("panic=%+v", got)
	}
}
