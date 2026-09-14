// Package admin 提供网关自带的 Web 管理界面：静态页面 + 管理 API。
//
// 设计要点：
//   - 页面本身（HTML/CSS/JS）**不鉴权**（浏览器无法在导航请求里带 Authorization），
//     但所有 /admin/api/* 一律校验同一个 api_key（api_key 为空时不鉴权，与网关一致）；
//     页面内的数据全部经 JS fetch 携带 Bearer key 获取，故未持 key 者拿不到任何数据。
//   - 本包不直接依赖 cmd/server 的 Config 类型：配置的读/写/重启以函数依赖（Deps）注入，
//     保持 internal 包与 main 的解耦（与 server.Config.StickyCount 同一手法）。
package admin

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/requestlog"
	"workbuddy2api/internal/upstream"
)

//go:embed static
var staticFS embed.FS

// maskedPlaceholder 密钥字段在 GET 时的打码值；PUT 时原样回传即代表「保持原值不变」。
const maskedPlaceholder = "***"

// secretPaths 需要打码的配置字段（点分路径）。
var secretPaths = []string{"api_key", "upstash.token"}

// Deps 管理界面的依赖，由 cmd/server 注入。
type Deps struct {
	Pool        *pool.Pool
	Upstream    *upstream.Client
	RequestLogs *requestlog.Store
	// APIKey 与网关鉴权同一把钥匙；空 = 不鉴权。
	APIKey string
	// AuthDir 账号凭证目录（添加账号时落盘位置）。
	AuthDir string
	// PricingFile 单价配置文件路径（估算 Token 价值用；空 = 不持久化）。
	PricingFile string
	// ConfigPath 配置文件路径（展示用）。
	ConfigPath string
	// ConfigFile 读取 config.json 原始对象（含明文密钥，仅进程内使用）。
	ConfigFile func() (map[string]any, error)
	// SaveConfig 校验（走与启动一致的 Load 管线）并原子写回 config.json。
	SaveConfig func(obj map[string]any) error
	// Effective 运行中生效配置快照（不含明文密钥）。
	Effective func() map[string]any
	// Restart 异步重启进程（重新拉起自身）。nil = 不支持。
	Restart func()
	// StartedAt 进程启动时刻。
	StartedAt time.Time
}

// Handler 管理界面入口（挂载在 /admin/ 下，已由上层 StripPrefix）。
type Handler struct {
	d       Deps
	api     *http.ServeMux
	assets  http.Handler
	now     func() time.Time
	pricing pricingState
	usage   usageStatsState
}

// New 构建管理界面 handler。
func New(d Deps) *Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// embed 路径写错属编程错误：退化为 404 页面服务，不影响网关主功能。
		sub = fs.FS(emptyFS{})
	}
	h := &Handler{
		d:      d,
		api:    http.NewServeMux(),
		assets: http.FileServer(http.FS(sub)),
		now:    time.Now,
	}
	h.pricing.file = d.PricingFile
	h.routes()
	return h
}

// emptyFS 兜底空文件系统（仅在 embed 失败时使用）。
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

func (h *Handler) routes() {
	h.api.HandleFunc("GET /api/usage-stats", h.getUsageStats)
	h.api.HandleFunc("GET /api/pricing", h.getPricing)
	h.api.HandleFunc("PUT /api/pricing", h.putPricing)
	h.api.HandleFunc("GET /api/performance", h.getPerformance)
	h.api.HandleFunc("GET /api/diagnostics", h.getDiagnostics)
	h.api.HandleFunc("GET /api/request-logs", h.getRequestLogs)
	h.api.HandleFunc("GET /api/config", h.getConfig)
	h.api.HandleFunc("PUT /api/config", h.putConfig)
	h.api.HandleFunc("POST /api/restart", h.restart)
	h.api.HandleFunc("GET /api/accounts/{uid}/credits", h.getAccountCredits)
	h.api.HandleFunc("GET /api/accounts/{uid}/requests", h.getAccountRequests)
	h.api.HandleFunc("POST /api/accounts/{uid}/revive", h.reviveAccount)
	h.api.HandleFunc("POST /api/accounts/{uid}/reset", h.resetAccount)
	h.api.HandleFunc("POST /api/login/start", h.loginStart)
	h.api.HandleFunc("GET /api/login/poll", h.loginPoll)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/api" || strings.HasPrefix(p, "/api/") {
		if !h.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]any{
					"message": "missing or invalid API key",
					"code":    "invalid_api_key",
				},
			})
			return
		}
		h.api.ServeHTTP(w, r)
		return
	}
	switch p {
	case "", "/":
		h.serveIndex(w, r)
	case "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	default:
		// 静态资源（app.js / style.css 等）不鉴权：不含任何敏感数据。
		w.Header().Set("Cache-Control", "no-cache")
		h.assets.ServeHTTP(w, r)
	}
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	raw, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "admin ui not embedded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

// authorized 校验 Bearer key（api_key 为空时直接放行，与网关全局口径一致）。
func (h *Handler) authorized(r *http.Request) bool {
	if h.d.APIKey == "" {
		return true
	}
	authz := r.Header.Get("Authorization")
	return strings.HasPrefix(authz, "Bearer ") && strings.TrimPrefix(authz, "Bearer ") == h.d.APIKey
}

// ---------------------------------------------------------------------------
// 配置
// ---------------------------------------------------------------------------

type configResponse struct {
	Path         string         `json:"path"`
	Exists       bool           `json:"exists"`
	File         map[string]any `json:"file"`
	Effective    map[string]any `json:"effective"`
	MaskedFields []string       `json:"masked_fields"`
	ReadError    string         `json:"read_error,omitempty"`
	CanRestart   bool           `json:"can_restart"`
	StartedAt    string         `json:"started_at"`
	ServerTime   string         `json:"server_time"`
	UptimeSec    int64          `json:"uptime_sec"`
}

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	resp := configResponse{
		Path:         h.d.ConfigPath,
		MaskedFields: secretPaths,
		CanRestart:   h.d.Restart != nil,
	}
	if h.d.ConfigFile != nil {
		obj, err := h.d.ConfigFile()
		if err != nil {
			resp.ReadError = err.Error()
		} else if obj != nil {
			resp.Exists = true
			resp.File = maskSecrets(obj, secretPaths)
		}
	}
	if h.d.Effective != nil {
		resp.Effective = h.d.Effective()
	}
	now := h.now()
	resp.ServerTime = now.Format(time.RFC3339)
	if !h.d.StartedAt.IsZero() {
		resp.StartedAt = h.d.StartedAt.Format(time.RFC3339)
		resp.UptimeSec = int64(now.Sub(h.d.StartedAt).Seconds())
	}
	writeJSON(w, http.StatusOK, resp)
}

// putConfig 保存配置：先与磁盘现状合并打码字段，再交给注入的 SaveConfig 校验 + 落盘。
// 返回 restart_required=true（绝大部分字段需要重启进程才生效）。
func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := readLimited(r, 2<<20) // 配置不超过 2MB
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var obj map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", "配置不是合法 JSON 对象："+err.Error())
		return
	}
	if len(obj) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_json", "配置为空对象")
		return
	}
	// 打码占位符还原：值等于 "***" 的字段保持磁盘上的原值（用户没改密钥）。
	restored := []string{}
	if h.d.ConfigFile != nil {
		if cur, err := h.d.ConfigFile(); err == nil {
			restored = mergeSecrets(obj, cur, secretPaths)
		}
	}
	if h.d.SaveConfig == nil {
		writeErr(w, http.StatusNotImplemented, "save_unavailable", "该构建不支持在线保存配置")
		return
	}
	if err := h.d.SaveConfig(obj); err != nil {
		writeErr(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	msg := "配置已写入 " + h.d.ConfigPath + "，需重启服务生效"
	if len(restored) > 0 {
		msg += "（密钥字段保持原值不变：" + strings.Join(restored, ", ") + "）"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"restart_required": true,
		"kept_secrets":     restored,
		"message":          msg,
	})
}

// restart 触发进程重启。先回响应（页面能收到），再由注入的 Restart 异步停机 + 重新拉起。
func (h *Handler) restart(w http.ResponseWriter, r *http.Request) {
	if h.d.Restart == nil {
		writeErr(w, http.StatusNotImplemented, "restart_unavailable", "该构建不支持在线重启")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "服务正在重启，页面将在数秒后自动重连…",
	})
	go func() {
		time.Sleep(400 * time.Millisecond) // 等响应真正写回浏览器
		h.d.Restart()
	}()
}

// ---------------------------------------------------------------------------
// 账号人工干预
// ---------------------------------------------------------------------------

// getAccountCredits 在服务端使用账号凭证查询，凭证不会返回浏览器。
func (h *Handler) getAccountCredits(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.d.Pool == nil || h.d.Upstream == nil {
		writeErr(w, http.StatusServiceUnavailable, "credits_unavailable", "积分查询不可用")
		return
	}
	a := h.d.Pool.AuthByUID(r.PathValue("uid"))
	if a == nil {
		writeErr(w, http.StatusNotFound, "unknown_account", "账号不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	summary, err := h.d.Upstream.FetchResourceSummary(ctx, a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "credits_query_failed", "积分查询失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) reviveAccount(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if h.d.Pool == nil {
		writeErr(w, http.StatusNotImplemented, "pool_unavailable", "账号池不可用")
		return
	}
	if _, ok := h.d.Pool.Status(uid); !ok {
		writeErr(w, http.StatusNotFound, "unknown_account", "账号不存在："+uid)
		return
	}
	h.d.Pool.ReviveDisabled(uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uid": uid, "action": "revive"})
}

func (h *Handler) resetAccount(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if h.d.Pool == nil {
		writeErr(w, http.StatusNotImplemented, "pool_unavailable", "账号池不可用")
		return
	}
	if _, ok := h.d.Pool.Status(uid); !ok {
		writeErr(w, http.StatusNotFound, "unknown_account", "账号不存在："+uid)
		return
	}
	h.d.Pool.ClearPenalty(uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uid": uid, "action": "reset"})
}

// ---------------------------------------------------------------------------
// 添加账号（OAuth 设备授权）
// ---------------------------------------------------------------------------

type loginStartResponse struct {
	State   string `json:"state"`
	AuthURL string `json:"auth_url"`
}

func (h *Handler) loginStart(w http.ResponseWriter, r *http.Request) {
	if h.d.Upstream == nil {
		writeErr(w, http.StatusNotImplemented, "upstream_unavailable", "上游客户端不可用")
		return
	}
	state, authURL, err := h.d.Upstream.OAuthStart()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "oauth_start_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, loginStartResponse{State: state, AuthURL: authURL})
}

// loginPoll 轮询授权结果；成功后把凭证落盘 auths/ 并热加载进账号池。
func (h *Handler) loginPoll(w http.ResponseWriter, r *http.Request) {
	if h.d.Upstream == nil {
		writeErr(w, http.StatusNotImplemented, "upstream_unavailable", "上游客户端不可用")
		return
	}
	state := r.URL.Query().Get("state")
	res, err := h.d.Upstream.OAuthPoll(state)
	if err != nil {
		if errors.Is(err, upstream.ErrOAuthPending) {
			writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
			return
		}
		writeErr(w, http.StatusBadGateway, "oauth_poll_failed", err.Error())
		return
	}
	if strings.TrimSpace(res.UID) == "" {
		writeErr(w, http.StatusBadGateway, "oauth_no_uid",
			"登录已返回 token，但未能取到 uid（账号信息接口异常），请重试登录")
		return
	}
	if strings.TrimSpace(h.d.AuthDir) == "" {
		writeErr(w, http.StatusInternalServerError, "auth_dir_missing", "auth_dir 未配置")
		return
	}

	expiresAt := h.now().Add(time.Duration(res.ExpiresIn) * time.Second).Unix()
	if res.ExpiresIn <= 0 {
		expiresAt = 0
	}
	a := &auth.Auth{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresAt:    expiresAt,
		Domain:       res.Domain,
		UID:          res.UID,
		EnterpriseID: res.EnterpriseID,
		Nickname:     res.Nickname,
	}
	if err := writeAuthFile(h.d.AuthDir, a); err != nil {
		writeErr(w, http.StatusInternalServerError, "auth_save_failed", err.Error())
		return
	}
	// 热加载：重新扫描 auths 目录对齐账号池（新增即入池，无需重启）。
	loaded := 0
	if auths, lerr := auth.LoadDir(h.d.AuthDir); lerr == nil {
		loaded = len(auths)
		if h.d.Pool != nil {
			h.d.Pool.SyncToDir(auths)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"uid":        res.UID,
		"nickname":   res.Nickname,
		"file":       authFileName(res.UID),
		"accounts":   loaded,
		"expires_at": expiresAt,
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "code": code},
	})
}

func readLimited(r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("请求体超过 %d KB 上限", limit>>10)
	}
	return body, nil
}

// maskSecrets 深拷贝 obj 并把 secretPaths 指向的字段替换为 maskedPlaceholder（非空时）。
func maskSecrets(obj map[string]any, paths []string) map[string]any {
	cp := deepCopy(obj)
	for _, p := range paths {
		if v, ok := getPath(cp, p); ok {
			if s, isStr := v.(string); isStr && s != "" {
				setPath(cp, p, maskedPlaceholder)
			}
		}
	}
	return cp
}

// mergeSecrets 把 incoming 中值为 maskedPlaceholder 的密钥字段还原为 current 的现值。
// 返回被还原的字段名列表。
func mergeSecrets(incoming, current map[string]any, paths []string) []string {
	kept := []string{}
	for _, p := range paths {
		v, ok := getPath(incoming, p)
		if !ok {
			continue
		}
		s, isStr := v.(string)
		if !isStr || s != maskedPlaceholder {
			continue
		}
		cur, ok := getPath(current, p)
		if !ok {
			deletePath(incoming, p)
			kept = append(kept, p+"（原值为空，已移除该字段）")
			continue
		}
		setPath(incoming, p, cur)
		kept = append(kept, p)
	}
	return kept
}

func getPath(obj map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var cur any = obj
	for i, part := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return cur, true
		}
	}
	return nil, false
}

func setPath(obj map[string]any, path string, val any) {
	parts := strings.Split(path, ".")
	cur := obj
	for i, part := range parts {
		if i == len(parts)-1 {
			cur[part] = val
			return
		}
		next, ok := cur[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
}

func deletePath(obj map[string]any, path string) {
	parts := strings.Split(path, ".")
	cur := obj
	for i, part := range parts {
		if i == len(parts)-1 {
			delete(cur, part)
			return
		}
		next, ok := cur[part].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

func deepCopy(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case map[string]any:
			out[k] = deepCopy(t)
		case []any:
			out[k] = deepCopySlice(t)
		default:
			out[k] = v
		}
	}
	return out
}

func deepCopySlice(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		switch t := v.(type) {
		case map[string]any:
			out[i] = deepCopy(t)
		case []any:
			out[i] = deepCopySlice(t)
		default:
			out[i] = v
		}
	}
	return out
}
