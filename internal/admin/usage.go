package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"workbuddy2api/internal/upstream"
)

func (h *Handler) getAccountRequests(w http.ResponseWriter, r *http.Request) {
	// 请求内容可能含敏感信息，禁止浏览器/代理缓存，也不写入本地日志。
	w.Header().Set("Cache-Control", "no-store")
	if h.d.Pool == nil || h.d.Upstream == nil {
		writeErr(w, http.StatusServiceUnavailable, "requests_unavailable", "请求记录查询不可用")
		return
	}
	a := h.d.Pool.AuthByUID(r.PathValue("uid"))
	if a == nil {
		writeErr(w, http.StatusNotFound, "unknown_account", "账号不存在")
		return
	}
	values := r.URL.Query()
	page, size := 1, 10
	if raw := values.Get("page"); raw != "" {
		page, _ = strconv.Atoi(raw)
	}
	if raw := values.Get("page_size"); raw != "" {
		size, _ = strconv.Atoi(raw)
	}
	q := upstream.RequestUsageQuery{
		StartTime: values.Get("start") + " 00:00:00",
		EndTime:   values.Get("end") + " 23:59:59",
		PageNum:   page,
		PageSize:  size,
	}
	if err := q.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result, err := h.d.Upstream.FetchRequestUsage(ctx, a, q)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "requests_query_failed", "请求记录查询失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
