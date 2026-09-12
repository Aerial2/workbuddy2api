package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"workbuddy2api/internal/auth"
)

// RequestUsageQuery 使用官网已验证的页码分页协议，时间为北京时间。
type RequestUsageQuery struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
	PageNum   int    `json:"pageNum"`
	PageSize  int    `json:"pageSize"`
}

func (q RequestUsageQuery) Validate() error {
	const layout = "2006-01-02 15:04:05"
	start, err := time.ParseInLocation(layout, q.StartTime, softRateResetLoc)
	if err != nil {
		return fmt.Errorf("开始时间格式无效")
	}
	end, err := time.ParseInLocation(layout, q.EndTime, softRateResetLoc)
	if err != nil || end.Before(start) {
		return fmt.Errorf("结束时间无效或早于开始时间")
	}
	if end.Sub(start) >= 32*24*time.Hour {
		return fmt.Errorf("日期间隔不能超过 31 天")
	}
	if q.PageNum < 1 || q.PageNum > 100000 || q.PageSize < 1 || q.PageSize > 50 {
		return fmt.Errorf("页码需在 1–100000 之间，每页条数需在 1–50 之间")
	}
	return nil
}

// RequestUsage 仅返回页面所需的账单字段；credit 保留官网小数精度。
type RequestUsage struct {
	RequestID   string       `json:"requestId"`
	Credit      *json.Number `json:"credit"`
	Model       string       `json:"model"`
	Client      string       `json:"client"`
	RequestTime string       `json:"requestTime"`
	InputTrunc  string       `json:"inputTrunc"`
	Input       string       `json:"input"`
}

type RequestUsagePage struct {
	Total int            `json:"total"`
	Data  []RequestUsage `json:"data"`
}

func (c *Client) FetchRequestUsage(ctx context.Context, a *auth.Auth, q RequestUsageQuery) (*RequestUsagePage, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.billingBase(a)+"/billing/meter/get-user-request-usage", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a.Lock()
	c.BillingHeaders(req, a)
	a.Unlock()
	data, err := c.doJSON(req)
	if err != nil {
		return nil, err
	}
	var result struct {
		Total *int            `json:"total"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("解析请求记录失败: %w", err)
	}
	if result.Total == nil || *result.Total < 0 || len(result.Data) == 0 {
		return nil, fmt.Errorf("请求记录响应缺少有效的 total/data")
	}
	page := &RequestUsagePage{Total: *result.Total}
	if err := json.Unmarshal(result.Data, &page.Data); err != nil {
		return nil, fmt.Errorf("解析请求列表失败: %w", err)
	}
	if page.Data == nil {
		page.Data = []RequestUsage{}
	}
	for _, row := range page.Data {
		if row.Credit != nil {
			value, err := row.Credit.Float64()
			if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				return nil, fmt.Errorf("请求记录包含无效的积分消耗")
			}
		}
	}
	return page, nil
}
