package admin

import (
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

func TestAccountCredits(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer secret-account-token" {
			t.Error("wrong upstream credential")
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"Packages":[{"CycleTotalCapacity":"500","CycleUsedCapacity":"379.83999961","CycleRemainCapacity":"120.16000039","CapacityUnit":"credits"}]}}`)
	}))
	defer srv.Close()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "user", AccessToken: "secret-account-token"})
	p.SetCredits("user", 999)
	c := upstream.New()
	c.BillingBaseCN = srv.URL
	h := New(Deps{Pool: p, Upstream: c, APIKey: "admin-key"})
	for _, tc := range []struct {
		name, uid, key string
		status         int
	}{
		{"unauthorized", "user", "", 401},
		{"wrong key", "user", "wrong", 401},
		{"unknown account", "missing", "admin-key", 404},
		{"success", "user", "admin-key", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/accounts/"+tc.uid+"/credits", nil)
			req.Header.Set("Authorization", "Bearer "+tc.key)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret-account-token") {
				t.Error("response leaked credentials")
			}
			if tc.status == 200 {
				for _, value := range []string{`"total":500`, `"used":379.83999961`, `"remain":120.16000039`} {
					if !strings.Contains(w.Body.String(), value) {
						t.Errorf("missing %s in %s", value, w.Body.String())
					}
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Error("billing response should not be cached by HTTP")
				}
			}
		})
	}
	if calls.Load() != 1 {
		t.Errorf("upstream called %d times; unauthorized/unknown account must not query", calls.Load())
	}
	if st, _ := p.Status("user"); st.Credits != 999 {
		t.Error("display query changed account scheduling credits")
	}

	t.Run("upstream failure", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer bad.Close()
		c.BillingBaseCN = bad.URL
		req := httptest.NewRequest(http.MethodGet, "/api/accounts/user/credits", nil)
		req.Header.Set("Authorization", "Bearer admin-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "credits_query_failed") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}
