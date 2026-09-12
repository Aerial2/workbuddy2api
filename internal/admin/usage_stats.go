package admin

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

var billingZone = time.FixedZone("UTC+8", 8*3600)

const billingPageCap = 200 // 10,000 rows per account; reaching the cap is explicitly incomplete.

type usageMetric struct {
	Requests int      `json:"requests"`
	Known    int      `json:"known"`
	Credits  float64  `json:"credits"`
	Average  *float64 `json:"average"`
}

func (m *usageMetric) add(credit *float64) {
	m.Requests++
	if credit != nil {
		m.Known++
		m.Credits += *credit
		avg := m.Credits / float64(m.Known)
		m.Average = &avg
	}
}

type usageGroup struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	usageMetric
}
type usageAccount struct {
	usageGroup
	Expected int      `json:"expected"` // -1 if the first page could not be obtained
	Pages    int      `json:"pages"`
	Complete bool     `json:"complete"`
	Issues   []string `json:"issues"`
}
type usageStats struct {
	Period    string    `json:"period"`
	Start     string    `json:"start"`
	End       string    `json:"end"`
	FetchedAt time.Time `json:"fetched_at"`
	Complete  bool      `json:"complete"`
	usageMetric
	Accounts []usageAccount `json:"accounts"`
	Models   []usageGroup   `json:"models"`
	Daily    []usageGroup   `json:"daily"`
}
type usageCacheEntry struct {
	data usageStats
	at   time.Time
}
type usageStatsState struct {
	mu    sync.Mutex
	cache map[string]usageCacheEntry
	busy  bool
}

type usageFetcher func(context.Context, *auth.Auth, upstream.RequestUsageQuery) (*upstream.RequestUsagePage, error)

// collectUsage retains only aggregates and deduplication IDs, never request or response text.
func collectUsage(ctx context.Context, now time.Time, period string, accounts []pool.Status, lookup func(string) *auth.Auth, fetch usageFetcher) usageStats {
	now = now.In(billingZone).Truncate(time.Second)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, billingZone)
	switch period {
	case "7d":
		start = start.AddDate(0, 0, -6)
	case "month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, billingZone)
	}
	result := usageStats{Period: period, Start: start.Format("2006-01-02 15:04:05"), End: now.Format("2006-01-02 15:04:05"), FetchedAt: now, Complete: true, Accounts: []usageAccount{}, Models: []usageGroup{}, Daily: []usageGroup{}}
	models := map[string]*usageGroup{}
	daily := map[string]*usageGroup{}
	for day := start; !day.After(now); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		daily[key] = &usageGroup{Key: key, Name: key}
	}
	for _, acct := range accounts {
		a := usageAccount{usageGroup: usageGroup{Key: acct.UID, Name: acct.Nickname}, Expected: -1, Complete: true, Issues: []string{}}
		issue := func(s string) {
			a.Complete = false
			for _, old := range a.Issues {
				if old == s {
					return
				}
			}
			a.Issues = append(a.Issues, s)
		}
		seen := map[string]bool{}
		credential := lookup(acct.UID)
		if credential == nil {
			issue("账号已移除，无法读取账单")
		} else {
			for page := 1; page <= billingPageCap; page++ {
				if ctx.Err() != nil {
					issue("查询超时或已取消，尚未读取全部分页")
					break
				}
				q := upstream.RequestUsageQuery{StartTime: result.Start, EndTime: result.End, PageNum: page, PageSize: 50}
				data, err := fetch(ctx, credential, q)
				if err != nil || data == nil {
					issue("账单分页查询失败，请稍后重试或检查账号登录状态")
					break
				}
				a.Pages++
				if a.Expected < 0 {
					a.Expected = data.Total
				} else if a.Expected != data.Total {
					issue("查询期间官网记录总数发生变化，请刷新重试")
				}
				if len(data.Data) > 50 {
					issue("官网返回了超出分页大小的记录")
					break
				}
				added := 0
				for _, row := range data.Data {
					if row.RequestID == "" || len(row.RequestID) > 512 {
						issue("存在缺失或无效请求 ID 的记录，已跳过")
						continue
					}
					if seen[row.RequestID] {
						issue("分页存在重复记录，已去重；可能有遗漏")
						continue
					}
					seen[row.RequestID] = true
					added++
					at, err := time.ParseInLocation("2006-01-02 15:04:05", row.RequestTime, billingZone)
					if err != nil || at.Before(start) || at.After(now) {
						issue("存在时间无效或超出查询范围的记录，已跳过")
						continue
					}
					var credit *float64
					if row.Credit != nil {
						n, err := row.Credit.Float64()
						if err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && !math.IsInf(result.Credits+n, 0) {
							credit = &n
						}
					}
					if credit == nil {
						issue("部分记录缺少有效积分；消耗和均值仅统计已知积分")
					}
					model := strings.TrimSpace(row.Model)
					if model == "" {
						model = "未提供模型"
						issue("部分记录未提供模型")
					}
					if len([]rune(model)) > 128 {
						model = string([]rune(model)[:128])
						issue("模型名称过长，已截断分组")
					}
					if models[model] == nil {
						models[model] = &usageGroup{Key: model, Name: model}
					}
					a.add(credit)
					result.add(credit)
					models[model].add(credit)
					daily[at.Format("2006-01-02")].add(credit)
				}
				if len(seen) >= data.Total {
					if len(seen) != data.Total {
						issue("已读取记录数与官网总数不一致")
					}
					break
				}
				if len(data.Data) < 50 || added == 0 {
					issue("分页提前结束，读取条数少于官网总数")
					break
				}
				if page == billingPageCap {
					issue("达到每账号 10000 条安全上限，统计尚不完整")
				}
			}
		}
		if !a.Complete {
			result.Complete = false
		}
		result.Accounts = append(result.Accounts, a)
	}
	for _, g := range models {
		result.Models = append(result.Models, *g)
	}
	sort.Slice(result.Models, func(i, j int) bool {
		if result.Models[i].Credits == result.Models[j].Credits {
			return result.Models[i].Key < result.Models[j].Key
		}
		return result.Models[i].Credits > result.Models[j].Credits
	})
	for _, g := range daily {
		result.Daily = append(result.Daily, *g)
	}
	sort.Slice(result.Daily, func(i, j int) bool { return result.Daily[i].Key < result.Daily[j].Key })
	return result
}

func (h *Handler) getUsageStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.d.Pool == nil || h.d.Upstream == nil {
		writeErr(w, 503, "unavailable", "官网用量统计不可用")
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "7d"
	}
	if period != "today" && period != "7d" && period != "month" {
		writeErr(w, 400, "invalid_period", "时间范围只能为 today、7d 或 month")
		return
	}
	accounts := h.d.Pool.List()
	if uid := r.URL.Query().Get("uid"); uid != "" {
		found := false
		for _, a := range accounts {
			if a.UID == uid {
				accounts = []pool.Status{a}
				found = true
				break
			}
		}
		if !found {
			writeErr(w, 404, "unknown_account", "账号不存在")
			return
		}
	}
	now := h.now()
	key := period + now.In(billingZone).Format("2006-01-02")
	for _, a := range accounts {
		key += fmt.Sprintf("|%s:%s", a.UID, a.Nickname)
	}
	h.usage.mu.Lock()
	if cached, ok := h.usage.cache[key]; ok && now.Sub(cached.at) < time.Minute && r.URL.Query().Get("refresh") != "1" {
		h.usage.mu.Unlock()
		writeJSON(w, 200, cached.data)
		return
	}
	if h.usage.busy {
		h.usage.mu.Unlock()
		w.Header().Set("Retry-After", "3")
		writeErr(w, 429, "stats_busy", "另一项官网统计正在查询，请稍后刷新")
		return
	}
	h.usage.busy = true
	h.usage.mu.Unlock()
	defer func() { h.usage.mu.Lock(); h.usage.busy = false; h.usage.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result := collectUsage(ctx, now, period, accounts, h.d.Pool.AuthByUID, h.d.Upstream.FetchRequestUsage)
	h.usage.mu.Lock()
	if len(h.usage.cache) >= 12 || h.usage.cache == nil {
		h.usage.cache = map[string]usageCacheEntry{}
	}
	h.usage.cache[key] = usageCacheEntry{data: result, at: h.now()}
	h.usage.mu.Unlock()
	writeJSON(w, 200, result)
}
