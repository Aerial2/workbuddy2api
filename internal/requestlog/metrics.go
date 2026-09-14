package requestlog

import (
	"math"
	"sort"
	"time"
)

type Metrics struct {
	Requests          int      `json:"requests"`
	Success           int      `json:"success"`
	SuccessRate       *float64 `json:"success_rate"`
	AverageMS         *float64 `json:"average_ms"`
	P95MS             *float64 `json:"p95_ms"`
	FirstFrameMS      *float64 `json:"first_frame_ms"`
	FirstFrameSamples int      `json:"first_frame_samples"`
	Retried           int      `json:"retried"`
	Retries           int      `json:"retries"`
	RetryRate         *float64 `json:"retry_rate"`
	Timeouts          int      `json:"timeouts"`
	RateLimits        int      `json:"rate_limits"`
}
type ModelMetrics struct {
	Model string `json:"model"`
	Metrics
}
type AccountMetrics struct {
	UID             string    `json:"uid"`
	Attempts        int       `json:"attempts"`
	Success         int       `json:"success"`
	SuccessRate     *float64  `json:"success_rate"`
	RetriedRequests int       `json:"retried_requests"`
	LastSuccess     time.Time `json:"last_success"`
	LastFailure     time.Time `json:"last_failure"`
	LastError       string    `json:"last_error"`
}
type Performance struct {
	Metrics
	Retained         int              `json:"retained"`
	Capacity         int              `json:"capacity"`
	Dropped          uint64           `json:"dropped"`
	Oldest           time.Time        `json:"oldest"`
	Latest           time.Time        `json:"latest"`
	Models           []ModelMetrics   `json:"models"`
	Accounts         []AccountMetrics `json:"accounts"`
	Slow             []Record         `json:"slow"`
	Pricing          Pricing          `json:"pricing"`
	Tokens           TokenMetrics     `json:"tokens"`
	AttemptsComplete bool             `json:"attempts_complete"`
}

func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := 100 * float64(n) / float64(d)
	return &v
}
func measure(rows []Record) Metrics {
	m := Metrics{Requests: len(rows)}
	if len(rows) == 0 {
		return m
	}
	durations := make([]int64, 0, len(rows))
	total, first := float64(0), float64(0)
	for _, r := range rows {
		if r.Result == "success" {
			m.Success++
		}
		if r.Retries > 0 {
			m.Retried++
			m.Retries += r.Retries
		}
		durations = append(durations, r.DurationMS)
		total += float64(r.DurationMS)
		if r.Mode == "stream" && r.FirstTokenMS > 0 {
			m.FirstFrameSamples++
			first += float64(r.FirstTokenMS)
		}
		for _, a := range r.AttemptDetails {
			if a.ErrorCode == "timeout" {
				m.Timeouts++
			}
			if a.ErrorCode == "soft_rate" {
				m.RateLimits++
			}
		}
		// Compatibility for records without detailed attempts: count only directly observable errors.
		if len(r.AttemptDetails) == 0 {
			if r.ErrorCode == "timeout" {
				m.Timeouts++
			}
			if r.ErrorCode == "soft_rate" {
				m.RateLimits++
			}
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	avg := total / float64(len(rows))
	p95 := float64(durations[int(math.Ceil(.95*float64(len(rows))))-1])
	m.AverageMS = &avg
	m.P95MS = &p95
	m.SuccessRate = ratio(m.Success, m.Requests)
	m.RetryRate = ratio(m.Retried, m.Requests)
	if m.FirstFrameSamples > 0 {
		avg := first / float64(m.FirstFrameSamples)
		m.FirstFrameMS = &avg
	}
	return m
}
func (s *Store) Performance(pricing Pricing) Performance {
	snap := s.Snapshot()
	allRows := append([]Record{}, snap.Data...)
	p := Performance{Metrics: measure(snap.Data), Retained: snap.Retained, Capacity: snap.Capacity, Dropped: snap.Dropped, Models: []ModelMetrics{}, Accounts: []AccountMetrics{}, Slow: []Record{}, AttemptsComplete: true}
	byModel := map[string][]Record{}
	accounts := map[string]*AccountMetrics{}
	for _, r := range snap.Data {
		if p.Oldest.IsZero() || r.StartedAt.Before(p.Oldest) {
			p.Oldest = r.StartedAt
		}
		if r.FinishedAt.After(p.Latest) {
			p.Latest = r.FinishedAt
		}
		model := r.Model
		if model == "" {
			model = "-"
		}
		byModel[model] = append(byModel[model], r)
		if len(r.AttemptDetails) != r.Attempts {
			p.AttemptsComplete = false
		}
		retried := map[string]bool{}
		for _, a := range r.AttemptDetails {
			if accounts[a.UID] == nil {
				accounts[a.UID] = &AccountMetrics{UID: a.UID}
			}
			m := accounts[a.UID]
			m.Attempts++
			if a.ErrorCode == "" {
				m.Success++
				if r.FinishedAt.After(m.LastSuccess) {
					m.LastSuccess = r.FinishedAt
				}
			} else if r.FinishedAt.After(m.LastFailure) || r.FinishedAt.Equal(m.LastFailure) {
				m.LastFailure = r.FinishedAt
				m.LastError = a.ErrorCode
			}
			if r.Retries > 0 && !retried[a.UID] {
				m.RetriedRequests++
				retried[a.UID] = true
			}
		}
	}
	for model, rows := range byModel {
		p.Models = append(p.Models, ModelMetrics{Model: model, Metrics: measure(rows)})
	}
	sort.Slice(p.Models, func(i, j int) bool {
		if p.Models[i].Requests == p.Models[j].Requests {
			return p.Models[i].Model < p.Models[j].Model
		}
		return p.Models[i].Requests > p.Models[j].Requests
	})
	for _, m := range accounts {
		m.SuccessRate = ratio(m.Success, m.Attempts)
		p.Accounts = append(p.Accounts, *m)
	}
	sort.Slice(p.Accounts, func(i, j int) bool { return p.Accounts[i].UID < p.Accounts[j].UID })
	sort.Slice(snap.Data, func(i, j int) bool {
		if snap.Data[i].DurationMS == snap.Data[j].DurationMS {
			return snap.Data[i].ID > snap.Data[j].ID
		}
		return snap.Data[i].DurationMS > snap.Data[j].DurationMS
	})
	if len(snap.Data) > 10 {
		snap.Data = snap.Data[:10]
	}
	p.Slow = snap.Data
	p.Pricing = pricing
	p.Tokens = tokenMetrics(allRows, pricing)
	return p
}
