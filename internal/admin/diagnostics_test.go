package admin

import (
	"net/http/httptest"
	"testing"
	"time"

	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/requestlog"
)

func TestDiagnosisIssuesAndRecovery(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)
	breaker := now.Add(2 * time.Hour)
	for _, tc := range []struct {
		name, code string
		status     pool.Status
		recent     requestlog.AccountMetrics
		recovery   bool
	}{
		{name: "ready", code: "ready"},
		{name: "session", code: "session_dead", status: pool.Status{Disabled: true, DisabledReason: "12153 session dead"}},
		{name: "credit", code: "hard_credit", status: pool.Status{CoolKind: "hard_credit", Until: later}, recovery: true},
		{name: "model", code: "model_rate", status: pool.Status{CoolKind: "soft_rate", SoftRateModel: "glm", Until: later}, recovery: true},
		{name: "timeout", code: "timeout", recent: requestlog.AccountMetrics{LastFailure: now, LastError: "timeout"}},
		{name: "transport", code: "transport", recent: requestlog.AccountMetrics{LastFailure: now, LastError: "transport"}},
		{name: "breaker", code: "breaker", status: pool.Status{BreakerUntil: breaker, Until: later, CoolKind: "soft_rate"}, recovery: true},
		{name: "recovered", code: "ready", recent: requestlog.AccountMetrics{LastFailure: now, LastSuccess: later, LastError: "timeout"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := diagnose(tc.status, tc.recent, now)
			if d.Issues[0].Code != tc.code || (d.RecoveryAt != nil) != tc.recovery {
				t.Fatalf("diagnosis=%+v", d)
			}
			if tc.name == "breaker" && !d.RecoveryAt.Equal(breaker) {
				t.Fatal("must wait for both cooldown and breaker")
			}
		})
	}
}
func TestDiagnosticAPIsAuthAndNoChat(t *testing.T) {
	// No upstream client is supplied: these APIs must only inspect local state.
	h := New(Deps{Pool: pool.New(""), RequestLogs: requestlog.New(), APIKey: "key"})
	for _, path := range []string{"/api/diagnostics", "/api/performance"} {
		for _, key := range []string{"", "key"} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 200
			if key == "" {
				want = 401
			}
			if w.Code != want {
				t.Fatalf("%s: %d", path, w.Code)
			}
			if want == 200 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cache allowed")
			}
		}
	}
}
