// Package requestlog stores bounded, in-memory chat request metadata only.
package requestlog

import (
	"strings"
	"sync"
	"time"
)

const Capacity = 1000

// Record intentionally has no prompt, response body, headers, token or raw error fields.
type Attempt struct {
	UID       string `json:"uid"`
	ErrorCode string `json:"error_code,omitempty"`
}

// NoteFailure records each failed attempt using a fixed classification only.
func (r *Record) NoteFailure(code string) {
	r.LastFailure = code
	if len(r.AttemptDetails) > 0 {
		r.AttemptDetails[len(r.AttemptDetails)-1].ErrorCode = code
	}
}

type Record struct {
	ID               uint64    `json:"id"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	DurationMS       int64     `json:"duration_ms"`
	FirstTokenMS     int64     `json:"first_token_ms"`
	CompletionTokens int       `json:"completion_tokens"` // -1 = upstream did not report usage
	Model            string    `json:"model"`
	Mode             string    `json:"mode"`
	UID              string    `json:"uid"`
	Accounts         []string  `json:"accounts"` // last 20 acquired account attempts
	AttemptDetails   []Attempt `json:"attempt_details"`
	Attempts         int       `json:"attempts"`
	Retries          int       `json:"retries"`
	Status           int       `json:"status"` // HTTP status; stream failures may still be 200
	Result           string    `json:"result"`
	ErrorCode        string    `json:"error_code,omitempty"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	LastFailure      string    `json:"last_failure,omitempty"`
}

type Filter struct {
	Before uint64
	Limit  int
	Model  string
	UID    string
	Result string
}

type Page struct {
	Data       []Record `json:"data"`
	Total      int      `json:"total"`
	Retained   int      `json:"retained"`
	Capacity   int      `json:"capacity"`
	Dropped    uint64   `json:"dropped"`
	HasMore    bool     `json:"has_more"`
	NextBefore uint64   `json:"next_before"`
}

type Store struct {
	mu    sync.RWMutex
	rows  [Capacity]Record
	next  int
	count int
	seq   uint64
}

func New() *Store { return &Store{} }

func bounded(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 128 {
		r = r[:128]
	}
	return string(r)
}

func (s *Store) Add(r Record) {
	if s == nil {
		return
	}
	r.Model, r.UID = bounded(r.Model), bounded(r.UID)
	if len(r.Accounts) > 20 {
		r.Accounts = r.Accounts[len(r.Accounts)-20:]
	}
	accounts := make([]string, len(r.Accounts))
	for i, uid := range r.Accounts {
		accounts[i] = bounded(uid)
	}
	r.Accounts = accounts
	if len(r.AttemptDetails) > 20 {
		r.AttemptDetails = r.AttemptDetails[len(r.AttemptDetails)-20:]
	}
	r.AttemptDetails = append([]Attempt{}, r.AttemptDetails...)
	for i := range r.AttemptDetails {
		r.AttemptDetails[i].UID = bounded(r.AttemptDetails[i].UID)
		if code := r.AttemptDetails[i].ErrorCode; code != "" && Describe(code) == "" {
			r.AttemptDetails[i].ErrorCode = "internal_error"
		}
	}
	// Only fixed, classified descriptions are retained; never store raw upstream error messages.
	if r.ErrorCode != "" && Describe(r.ErrorCode) == "" {
		r.ErrorCode = "internal_error"
	}
	r.ErrorMessage = Describe(r.ErrorCode)
	if r.LastFailure != "" {
		r.LastFailure = Describe(r.LastFailure)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	r.ID = s.seq
	s.rows[s.next] = r
	s.next = (s.next + 1) % Capacity
	if s.count < Capacity {
		s.count++
	}
}

func (s *Store) Query(f Filter) Page {
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 50
	}
	p := Page{Data: []Record{}, Capacity: Capacity}
	if s == nil {
		return p
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p.Retained, p.Dropped = s.count, s.seq-uint64(s.count)
	model := strings.ToLower(f.Model)
	for i := 0; i < s.count; i++ {
		r := s.rows[(s.next-1-i+Capacity)%Capacity]
		if f.Result != "" && r.Result != f.Result {
			continue
		}
		if model != "" && !strings.Contains(strings.ToLower(r.Model), model) {
			continue
		}
		if f.UID != "" {
			found := r.UID == f.UID
			for _, uid := range r.Accounts {
				found = found || uid == f.UID
			}
			if !found {
				continue
			}
		}
		p.Total++
		if f.Before != 0 && r.ID >= f.Before {
			continue
		}
		if len(p.Data) >= f.Limit {
			p.HasMore = true
			continue
		}
		r.Accounts = append([]string{}, r.Accounts...)
		r.AttemptDetails = append([]Attempt{}, r.AttemptDetails...)
		p.Data = append(p.Data, r)
		p.NextBefore = r.ID
	}
	if !p.HasMore {
		p.NextBefore = 0
	}
	return p
}

// Snapshot returns a single consistent view of all retained records, newest first.
func (s *Store) Snapshot() Page {
	p := Page{Data: []Record{}, Capacity: Capacity}
	if s == nil {
		return p
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p.Retained = s.count
	p.Total = s.count
	p.Dropped = s.seq - uint64(s.count)
	for i := 0; i < s.count; i++ {
		r := s.rows[(s.next-1-i+Capacity)%Capacity]
		r.Accounts = append([]string{}, r.Accounts...)
		r.AttemptDetails = append([]Attempt{}, r.AttemptDetails...)
		p.Data = append(p.Data, r)
	}
	return p
}

// Describe deliberately does not accept arbitrary upstream text.
func Describe(code string) string {
	switch code {
	case "":
		return ""
	case "invalid_api_key":
		return "API Key 缺失或无效"
	case "invalid_request":
		return "请求体读取失败"
	case "request_body_too_large":
		return "请求体超过大小上限"
	case "no_healthy_account":
		return "所有账号不可用或重试次数已用尽"
	case "refresh_failed":
		return "账号登录凭证刷新失败"
	case "session_dead":
		return "账号登录状态失效，需要重新登录"
	case "hard_credit":
		return "账号积分不足"
	case "soft_rate":
		return "上游请求限流"
	case "not_found":
		return "上游接口返回 404"
	case "server":
		return "上游服务异常"
	case "content_blocked":
		return "上游内容策略拦截"
	case "bad_params":
		return "上游拒绝请求参数"
	case "client":
		return "上游拒绝请求"
	case "timeout":
		return "上游请求超时"
	case "transport":
		return "上游连接失败"
	case "cancelled":
		return "客户端取消请求或连接断开"
	case "stream_error":
		return "流式传输失败或上游返回空流"
	case "upstream_parse":
		return "上游响应解析失败"
	case "upstream_event_error":
		return "上游数据流包含错误事件"
	case "internal_error":
		return "服务内部异常"
	default:
		return ""
	}
}
