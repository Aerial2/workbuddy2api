package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func TestAccountRequests(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var q upstream.RequestUsageQuery
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.PageNum != 2 || q.PageSize != 10 || q.StartTime != "2026-09-05 00:00:00" || q.EndTime != "2026-09-12 23:59:59" {
			t.Errorf("incorrect query: %+v, err=%v", q, err)
		}
		if r.Header.Get("X-User-Id") != "user" {
			t.Error("wrong account")
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"total":11,"data":[{"requestId":"crb-test","credit":0.21,"input":"hello"}]}}`)
	}))
	defer srv.Close()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "user", AccessToken: "private-token"})
	c := upstream.New()
	c.BillingBaseCN = srv.URL
	h := New(Deps{Pool: p, Upstream: c, APIKey: "admin-key"})
	query := "?start=2026-09-05&end=2026-09-12&page=2&page_size=10"
	for _, tc := range []struct {
		name, uid, key, query string
		status                int
	}{
		{"success", "user", "admin-key", query, 200},
		{"unauthorized", "user", "", query, 401},
		{"wrong key", "user", "wrong", query, 401},
		{"unknown", "missing", "admin-key", query, 404},
		{"missing dates", "user", "admin-key", "", 400},
		{"bad page", "user", "admin-key", "?start=2026-09-05&end=2026-09-12&page=no", 400},
		{"oversize page", "user", "admin-key", "?start=2026-09-05&end=2026-09-12&page_size=1000", 400},
		{"long date range", "user", "admin-key", "?start=2026-01-01&end=2026-09-12", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/accounts/"+tc.uid+"/requests"+tc.query, nil)
			r.Header.Set("Authorization", "Bearer "+tc.key)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-token") {
				t.Error("credential leaked")
			}
			if tc.status == 200 && (!strings.Contains(w.Body.String(), `"credit":0.21`) || w.Header().Get("Cache-Control") != "no-store") {
				t.Error("wrong response/cache policy")
			}
		})
	}
	if calls.Load() != 1 {
		t.Errorf("unexpected upstream calls: %d", calls.Load())
	}
	fail.Store(true)
	r := httptest.NewRequest(http.MethodGet, "/api/accounts/user/requests"+query, nil)
	r.Header.Set("Authorization", "Bearer admin-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "requests_query_failed") {
		t.Fatalf("unexpected error response: %d %s", w.Code, w.Body.String())
	}
}
