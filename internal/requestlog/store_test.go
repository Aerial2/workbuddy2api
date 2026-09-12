package requestlog

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestBoundedStoreAndCursor(t *testing.T) {
	s := New()
	for i := 0; i < Capacity+5; i++ {
		s.Add(Record{Model: "model", Result: "success", UID: "final", Accounts: []string{"first", "final"}})
	}
	p := s.Query(Filter{Limit: 20})
	if p.Retained != Capacity || p.Dropped != 5 || len(p.Data) != 20 || p.Data[0].ID != 1005 || !p.HasMore {
		t.Fatalf("bad page: %+v", p)
	}
	s.Add(Record{})
	next := s.Query(Filter{Before: p.NextBefore, Limit: 20})
	if next.Data[0].ID != p.NextBefore-1 {
		t.Fatal("cursor duplicated or skipped rows")
	}
	if got := s.Query(Filter{UID: "first", Model: "ODEL", Result: "success"}); got.Total != 999 {
		t.Fatalf("filter total=%d", got.Total)
	}
	if got := s.Query(Filter{Before: 1}); len(got.Data) != 0 || got.HasMore || got.NextBefore != 0 {
		t.Fatal("empty cursor page")
	}
}

func TestStoreCopiesAndPrivacy(t *testing.T) {
	s := New()
	r := Record{Model: strings.Repeat("模", 200), Accounts: []string{"uid"}, ErrorCode: "transport", ErrorMessage: "secret prompt", LastFailure: "soft_rate"}
	s.Add(r)
	r.Accounts[0] = "mutated"
	p := s.Query(Filter{})
	got := p.Data[0]
	if len([]rune(got.Model)) != 128 || got.Accounts[0] != "uid" || strings.Contains(got.ErrorMessage, "secret") || got.LastFailure != Describe("soft_rate") {
		t.Fatalf("unsafe copy: %+v", got)
	}
	got.Accounts[0] = "modified"
	if s.Query(Filter{}).Data[0].Accounts[0] != "uid" {
		t.Fatal("query aliases store")
	}
	var absent *Store
	absent.Add(Record{})
	if absent.Query(Filter{}).Data == nil {
		t.Fatal("nil store must return empty array")
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for j := 0; j < 8; j++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				s.Add(Record{UID: fmt.Sprint(n)})
				s.Query(Filter{Limit: 10})
			}
		}(j)
	}
	wg.Wait()
	p := s.Query(Filter{})
	if p.Retained != 1000 || p.Dropped != 1400 || p.Data[0].ID != 2400 {
		t.Fatalf("concurrent counts: %+v", p)
	}
}
