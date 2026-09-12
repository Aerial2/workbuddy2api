package admin

import (
	"net/http"
	"strings"
	"time"

	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/requestlog"
)

type diagnosticIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Advice  string `json:"advice"`
}
type accountDiagnostic struct {
	Status     pool.Status               `json:"status"`
	Recent     requestlog.AccountMetrics `json:"recent"`
	Issues     []diagnosticIssue         `json:"issues"`
	RecoveryAt *time.Time                `json:"recovery_at"`
}

func diagnose(st pool.Status, recent requestlog.AccountMetrics, now time.Time) accountDiagnostic {
	d := accountDiagnostic{Status: st, Recent: recent, Issues: []diagnosticIssue{}}
	add := func(code, msg, advice string) { d.Issues = append(d.Issues, diagnosticIssue{code, msg, advice}) }
	if st.Disabled {
		if strings.Contains(st.DisabledReason, "session") || strings.Contains(st.DisabledReason, "12153") {
			add("session_dead", "登录状态失效，账号已禁用", "重新登录添加该账号；单纯清冷却不能修复登录凭证")
		} else {
			add("disabled", "账号已禁用", "查看禁用原因，确认凭证有效后再手动复活")
		}
	}
	recovery := st.Until
	if st.BreakerUntil.After(now) {
		add("breaker", "连续失败触发熔断", "等待熔断结束并检查上游状态，避免反复清处罚重试")
		if st.BreakerUntil.After(recovery) {
			recovery = st.BreakerUntil
		}
	}
	if st.Until.After(now) {
		switch {
		case st.CoolKind == "hard_credit":
			add("hard_credit", "积分不足，账号处于硬冷却", "查询官网剩余积分；补充额度或等待签到恢复")
		case st.SoftRateModel != "":
			add("model_rate", "模型限流："+st.SoftRateModel, "切换其他模型或等待该模型额度重置")
		case strings.Contains(st.Reason, "404"):
			add("not_found", "上游接口暂时不可用", "等待短冷却结束，持续出现时检查上游接口")
		default:
			add("soft_rate", "上游限流，账号处于软冷却", "降低并发或等待冷却结束")
		}
	}
	if !st.Disabled && recovery.After(now) {
		d.RecoveryAt = &recovery
	}
	if recent.LastFailure.After(recent.LastSuccess) {
		switch recent.LastError {
		case "timeout":
			add("timeout", "最近一次账号尝试超时", "检查上游响应速度和网络；在配置页查看首字节及空闲超时设置")
		case "transport":
			add("transport", "最近一次上游连接失败", "检查网络、代理和上游服务可达性")
		case "session_dead", "refresh_failed":
			if !st.Disabled {
				add(recent.LastError, requestlog.Describe(recent.LastError), "检查登录凭证，必要时重新登录")
			}
		default:
			if len(d.Issues) == 0 {
				add(recent.LastError, requestlog.Describe(recent.LastError), "查看实时日志详情；轻量查询成功不代表聊天链路正常")
			}
		}
	}
	if len(d.Issues) == 0 {
		add("ready", "当前没有账号池阻断", "这是状态诊断；如需验证聊天，请手动使用聊天测试（会消耗积分）")
	}
	return d
}
func (h *Handler) getPerformance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"started_at": h.d.StartedAt, "generated_at": h.now(), "stats": h.d.RequestLogs.Performance()})
}
func (h *Handler) getDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.d.Pool == nil {
		writeErr(w, 503, "unavailable", "账号池不可用")
		return
	}
	now := h.now()
	p := h.d.RequestLogs.Performance()
	byUID := map[string]requestlog.AccountMetrics{}
	for _, a := range p.Accounts {
		byUID[a.UID] = a
	}
	rows := []accountDiagnostic{}
	for _, st := range h.d.Pool.List() {
		a := byUID[st.UID]
		a.UID = st.UID
		rows = append(rows, diagnose(st, a, now))
	}
	writeJSON(w, 200, map[string]any{"accounts": rows, "retained": p.Retained, "dropped": p.Dropped, "attempts_complete": p.AttemptsComplete, "generated_at": now})
}
