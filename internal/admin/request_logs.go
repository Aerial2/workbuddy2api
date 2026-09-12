package admin

import (
	"net/http"
	"strconv"
	"strings"

	"workbuddy2api/internal/requestlog"
)

func (h *Handler) getRequestLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	f := requestlog.Filter{Limit: 50, Model: strings.TrimSpace(q.Get("model")), UID: strings.TrimSpace(q.Get("uid")), Result: q.Get("result")}
	if f.Result != "" && f.Result != "success" && f.Result != "failed" {
		writeErr(w, http.StatusBadRequest, "invalid_filter", "结果只能是 success 或 failed")
		return
	}
	if len([]rune(f.Model)) > 128 || len([]rune(f.UID)) > 128 {
		writeErr(w, http.StatusBadRequest, "invalid_filter", "筛选值最多 128 个字符")
		return
	}
	if value, ok := q["limit"]; ok {
		n, err := strconv.Atoi(value[0])
		if err != nil || n < 1 || n > 100 {
			writeErr(w, http.StatusBadRequest, "invalid_filter", "每页条数必须在 1 至 100 之间")
			return
		}
		f.Limit = n
	}
	if value, ok := q["before"]; ok {
		n, err := strconv.ParseUint(value[0], 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_filter", "分页游标无效")
			return
		}
		f.Before = n
	}
	writeJSON(w, http.StatusOK, h.d.RequestLogs.Query(f))
}
