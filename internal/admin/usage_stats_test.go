package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func usageRow(id int, credit string) upstream.RequestUsage {
	var n *json.Number
	if credit != "" {
		v := json.Number(credit)
		n = &v
	}
	return upstream.RequestUsage{RequestID: fmt.Sprint(id), Credit: n, RequestTime: "2026-09-12 10:00:00", Model: "model-a", Input: "PRIVATE-PROMPT"}
}
func usageNow() time.Time               { return time.Date(2026, 9, 12, 12, 0, 0, 0, billingZone) }
func usageLookup(uid string) *auth.Auth { return &auth.Auth{UID: uid} }

func TestUsageStatsFullPagination(t *testing.T) {
	calls := 0
	result := collectUsage(context.Background(), usageNow(), "7d", []pool.Status{{UID: "a"}, {UID: "b"}}, usageLookup, func(ctx context.Context, a *auth.Auth, q upstream.RequestUsageQuery) (*upstream.RequestUsagePage, error) {
		calls++
		if q.StartTime != "2026-09-06 00:00:00" || q.EndTime != "2026-09-12 12:00:00" || q.PageSize != 50 {
			t.Fatalf("range=%+v", q)
		}
		rows := []upstream.RequestUsage{}
		start := (q.PageNum - 1) * 50
		for i := start; i < start+50 && i < 53; i++ {
			row := usageRow(i, "0.21")
			if i%2 == 0 {
				row.Model = "model-b"
			}
			rows = append(rows, row)
		}
		return &upstream.RequestUsagePage{Total: 53, Data: rows}, nil
	})
	if calls != 4 || result.Requests != 106 || result.Known != 106 || !result.Complete || len(result.Models) != 2 || len(result.Daily) != 7 {
		t.Fatalf("bad aggregation: %+v calls=%d", result, calls)
	}
	if math.Abs(result.Credits-22.26) > 1e-9 || result.Average == nil || math.Abs(*result.Average-.21) > 1e-9 {
		t.Fatalf("credits=%v average=%v", result.Credits, result.Average)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatal("request text leaked")
	}
}

func TestUsageStatsIncomplete(t *testing.T) {
	for _, kind := range []string{"failure", "duplicate", "changed", "missing-credit", "invalid-time", "early-end", "missing-id", "negative-credit"} {
		t.Run(kind, func(t *testing.T) {
			fetch := func(_ context.Context, _ *auth.Auth, q upstream.RequestUsageQuery) (*upstream.RequestUsagePage, error) {
				if q.PageNum == 1 {
					rows := []upstream.RequestUsage{}
					for i := 0; i < 50; i++ {
						rows = append(rows, usageRow(i, "1"))
					}
					switch kind {
					case "missing-credit":
						rows[0].Credit = nil
					case "invalid-time":
						rows[0].RequestTime = "2026-08-12 10:00:00"
					case "missing-id":
						rows[0].RequestID = ""
					case "negative-credit":
						v := json.Number("-1")
						rows[0].Credit = &v
					}
					return &upstream.RequestUsagePage{Total: 51, Data: rows}, nil
				}
				switch kind {
				case "failure":
					return nil, errors.New("PRIVATE upstream error")
				case "duplicate":
					return &upstream.RequestUsagePage{Total: 51, Data: []upstream.RequestUsage{usageRow(0, "1")}}, nil
				case "changed":
					return &upstream.RequestUsagePage{Total: 52, Data: []upstream.RequestUsage{usageRow(50, "1"), usageRow(51, "1")}}, nil
				case "early-end":
					return &upstream.RequestUsagePage{Total: 51, Data: []upstream.RequestUsage{}}, nil
				default:
					return &upstream.RequestUsagePage{Total: 51, Data: []upstream.RequestUsage{usageRow(50, "1")}}, nil
				}
			}
			r := collectUsage(context.Background(), usageNow(), "today", []pool.Status{{UID: "a"}}, usageLookup, fetch)
			if r.Complete || r.Accounts[0].Complete || len(r.Accounts[0].Issues) == 0 {
				t.Fatalf("expected incomplete: %+v", r)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "PRIVATE") {
				t.Fatal("raw error leaked")
			}
			if kind == "missing-credit" && (r.Requests != 51 || r.Known != 50 || r.Credits != 50 || *r.Average != 1) {
				t.Fatalf("missing credit treated as zero: %+v", r)
			}
			if kind == "duplicate" && r.Requests != 50 {
				t.Fatal("duplicate counted twice")
			}
		})
	}
}

func TestUsageStatsLimitCancelAndDates(t *testing.T) {
	calls := 0
	fetch := func(_ context.Context, _ *auth.Auth, q upstream.RequestUsageQuery) (*upstream.RequestUsagePage, error) {
		calls++
		rows := []upstream.RequestUsage{}
		for i := 0; i < 50; i++ {
			rows = append(rows, usageRow((q.PageNum-1)*50+i, "0"))
		}
		return &upstream.RequestUsagePage{Total: 10001, Data: rows}, nil
	}
	r := collectUsage(context.Background(), usageNow(), "month", []pool.Status{{UID: "a"}}, usageLookup, fetch)
	if r.Complete || calls != billingPageCap || r.Requests != 10000 || r.Start != "2026-09-01 00:00:00" {
		t.Fatalf("limit/date wrong: calls=%d requests=%d", calls, r.Requests)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = collectUsage(ctx, usageNow(), "today", []pool.Status{{UID: "a"}}, usageLookup, fetch)
	if r.Complete || r.Requests != 0 || r.Accounts[0].Expected != -1 || calls != billingPageCap {
		t.Fatal("cancellation ignored")
	}
	r = collectUsage(context.Background(), time.Date(2026, 1, 1, 0, 30, 0, 0, billingZone), "7d", nil, usageLookup, fetch)
	if r.Start != "2025-12-26 00:00:00" || len(r.Daily) != 7 {
		t.Fatalf("year boundary: %+v", r)
	}
}

type statsTransport func(*http.Request) (*http.Response, error)

func (f statsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUsageStatsAPIAuthCacheAndBusy(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u", ExpiresAt: 9999999999})
	calls := 0
	up := upstream.New()
	up.HTTP = &http.Client{Transport: statsTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"total":0,"data":[]}}`))}, nil
	})}
	h := New(Deps{Pool: p, Upstream: up, APIKey: "key"})
	h.now = usageNow
	request := func(path, key string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if request("/api/usage-stats", "") != 401 || calls != 0 {
		t.Fatal("auth bypass")
	}
	if request("/api/usage-stats?period=bad", "key") != 400 || request("/api/usage-stats?uid=missing", "key") != 404 {
		t.Fatal("validation failed")
	}
	if request("/api/usage-stats?period=today", "key") != 200 || request("/api/usage-stats?period=today", "key") != 200 || calls != 1 {
		t.Fatalf("cache calls=%d", calls)
	}
	request("/api/usage-stats?period=today&refresh=1", "key")
	if calls != 2 {
		t.Fatal("manual refresh cached")
	}
	h.usage.busy = true
	if request("/api/usage-stats?period=month", "key") != 429 {
		t.Fatal("concurrency gate failed")
	}
}
