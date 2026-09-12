package admin

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"workbuddy2api/internal/requestlog"
)

func TestRequestLogsAuthAndFilters(t *testing.T) {
	s := requestlog.New()
	s.Add(requestlog.Record{UID: "u", Model: "glm-test", Result: "failed", ErrorCode: "stream_error"})
	h := New(Deps{APIKey: "test-key", RequestLogs: s})
	for _, tc := range []struct {
		query, key string
		status     int
	}{
		{"", "", 401}, {"", "wrong", 401}, {"", "test-key", 200},
		{"?result=failed&uid=u&model=GLM&limit=1", "test-key", 200},
		{"?result=bad", "test-key", 400}, {"?limit=0", "test-key", 400}, {"?limit=101", "test-key", 400},
		{"?limit=", "test-key", 400}, {"?before=-1", "test-key", 400}, {"?before=no", "test-key", 400},
	} {
		r := httptest.NewRequest("GET", "/api/request-logs"+tc.query, nil)
		r.Header.Set("Authorization", "Bearer "+tc.key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var p requestlog.Page
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if len(p.Data) != 1 || p.Data[0].ErrorMessage == "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("bad response: %s", w.Body.String())
			}
		}
	}
}
