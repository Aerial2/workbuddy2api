package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"workbuddy2api/internal/auth"
)

func TestFetchRequestUsage(t *testing.T) {
	q := RequestUsageQuery{StartTime: "2026-09-05 00:00:00", EndTime: "2026-09-12 23:59:59", PageNum: 2, PageSize: 10}
	cases := []struct {
		name, body string
		status     int
		wantErr    bool
	}{
		{"records", `{"code":0,"data":{"total":11,"data":[{"requestId":"crb-test","credit":0.21,"model":"deepseek-v4.1-flash","client":"","requestTime":"2026-09-12 18:07:00","inputTrunc":"<conversation_summary>","input":"<script>alert(1)</script>"}]}}`, 200, false},
		{"string credits", `{"code":0,"data":{"total":1,"data":[{"credit":"0.12345678"}]}}`, 200, false},
		{"missing credits", `{"code":0,"data":{"total":1,"data":[{}]}}`, 200, false},
		{"empty", `{"code":0,"data":{"total":0,"data":[]}}`, 200, false},
		{"null list", `{"code":0,"data":{"total":0,"data":null}}`, 200, false},
		{"missing data", `{"code":0,"data":{"total":0}}`, 200, true},
		{"missing total", `{"code":0,"data":{"data":[]}}`, 200, true},
		{"invalid list", `{"code":0,"data":{"total":1,"data":{}}}`, 200, true},
		{"invalid credits", `{"code":0,"data":{"total":1,"data":[{"credit":"bad"}]}}`, 200, true},
		{"overflow credits", `{"code":0,"data":{"total":1,"data":[{"credit":1e999}]}}`, 200, true},
		{"business error", `{"code":10001,"msg":"invalid params"}`, 200, true},
		{"unauthorized", `{"code":12153,"msg":"expired"}`, 401, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/billing/meter/get-user-request-usage" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-User-Id") != "user" || r.Header.Get("X-Tenant-Id") != "tenant" {
					t.Error("missing account authentication")
				}
				var got RequestUsageQuery
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != q {
					t.Errorf("query=%+v, error=%v", got, err)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := New()
			c.BillingBaseCN = srv.URL
			page, err := c.FetchRequestUsage(context.Background(), &auth.Auth{UID: "user", AccessToken: "secret", EnterpriseID: "tenant"}, q)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && page.Data == nil {
				t.Error("empty list must serialize as []")
			}
			if tc.name == "records" {
				if page.Total != 11 || len(page.Data) != 1 || page.Data[0].Credit.String() != "0.21" || page.Data[0].Input != "<script>alert(1)</script>" {
					t.Fatalf("incorrect record mapping: %+v", page)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := c.FetchRequestUsage(ctx, &auth.Auth{}, q); err == nil {
				t.Error("cancelled context should fail")
			}
		})
	}
}

func TestRequestUsageQueryValidation(t *testing.T) {
	valid := RequestUsageQuery{StartTime: "2026-09-01 00:00:00", EndTime: "2026-10-02 23:59:59", PageNum: 1, PageSize: 50}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RequestUsageQuery){
		func(q *RequestUsageQuery) { q.StartTime = "invalid" },
		func(q *RequestUsageQuery) { q.EndTime = "2026-02-30 23:59:59" },
		func(q *RequestUsageQuery) { q.EndTime = "2026-08-31 23:59:59" },
		func(q *RequestUsageQuery) { q.EndTime = "2026-10-03 23:59:59" },
		func(q *RequestUsageQuery) { q.PageNum = 0 },
		func(q *RequestUsageQuery) { q.PageSize = 0 },
		func(q *RequestUsageQuery) { q.PageSize = 51 },
	} {
		q := valid
		mutate(&q)
		if err := q.Validate(); err == nil {
			t.Errorf("accepted invalid query %+v", q)
		}
	}
}
