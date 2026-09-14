package requestlog

import (
	"math"
	"testing"
	"time"
)

func TestPerformanceMetrics(t *testing.T) {
	s := New()
	now := time.Now()
	s.Add(Record{StartedAt: now, FinishedAt: now.Add(time.Second), Model: "a", UID: "good", Result: "success", DurationMS: 1000, Mode: "stream", FirstTokenMS: 100, Attempts: 2, Retries: 1, AttemptDetails: []Attempt{{UID: "bad", ErrorCode: "soft_rate"}, {UID: "good"}}})
	s.Add(Record{StartedAt: now, FinishedAt: now.Add(3 * time.Second), Model: "a", UID: "bad", Result: "failed", ErrorCode: "timeout", DurationMS: 3000, Attempts: 1, AttemptDetails: []Attempt{{UID: "bad", ErrorCode: "timeout"}}})
	s.Add(Record{StartedAt: now, FinishedAt: now.Add(2 * time.Second), Model: "b", UID: "good", Result: "success", DurationMS: 2000, Attempts: 1, AttemptDetails: []Attempt{{UID: "good"}}})
	p := s.Performance(Pricing{})
	if p.Requests != 3 || p.Success != 2 || p.Retried != 1 || p.Retries != 1 || p.Timeouts != 1 || p.RateLimits != 1 || !p.AttemptsComplete {
		t.Fatalf("metrics=%+v", p)
	}
	if *p.AverageMS != 2000 || *p.P95MS != 3000 || *p.FirstFrameMS != 100 || p.FirstFrameSamples != 1 || math.Abs(*p.RetryRate-100.0/3) > 1e-9 {
		t.Fatalf("timing=%+v", p.Metrics)
	}
	if p.Slow[0].DurationMS != 3000 || len(p.Models) != 2 || p.Models[0].Requests != 2 {
		t.Fatal("model/slow ranking incorrect")
	}
	if len(p.Accounts) != 2 || p.Accounts[0].UID != "bad" || p.Accounts[0].Attempts != 2 || p.Accounts[0].Success != 0 || p.Accounts[0].RetriedRequests != 1 || p.Accounts[0].LastError != "timeout" {
		t.Fatalf("account attribution=%+v", p.Accounts)
	}
	snap := s.Snapshot()
	snap.Data[0].AttemptDetails[0].UID = "mutated"
	if s.Snapshot().Data[0].AttemptDetails[0].UID == "mutated" {
		t.Fatal("snapshot aliases attempts")
	}
}
func TestPerformanceEmptyAndRetainedOnly(t *testing.T) {
	s := New()
	p := s.Performance(Pricing{})
	if p.SuccessRate != nil || p.P95MS != nil || p.FirstFrameMS != nil || p.RetryRate != nil || p.Requests != 0 {
		t.Fatal("empty samples became zero percentages")
	}
	for i := 0; i < 1005; i++ {
		s.Add(Record{Result: "success", DurationMS: int64(i)})
	}
	p = s.Performance(Pricing{})
	if p.Requests != 1000 || p.Dropped != 5 || len(p.Slow) != 10 || p.Slow[0].DurationMS != 1004 {
		t.Fatalf("retention=%+v", p)
	}
	s.Add(Record{Attempts: 2, UID: "old"})
	if s.Performance(Pricing{}).AttemptsComplete {
		t.Fatal("missing attempt details not marked incomplete")
	}
}
