// main.go workbuddy2api 入口：加载配置、构建 pool、起调度器与 HTTP 服务。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"workbuddy2api/internal/admin"
	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/redisstore"
	"workbuddy2api/internal/requestlog"
	"workbuddy2api/internal/scheduler"
	"workbuddy2api/internal/server"
	"workbuddy2api/internal/session"
	"workbuddy2api/internal/upstream"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config json")
	flag.Parse()

	cfg, err := Load(*cfgPath)
	if err != nil {
		// 配置文件不存在时给一次机会用纯默认 + env
		if os.IsNotExist(err) {
			log.Printf("config %s not found, using defaults+env", *cfgPath)
			cfg, err = Load("")
		}
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
	}

	auths, err := auth.LoadDir(cfg.AuthDir)
	if err != nil {
		log.Fatalf("load auths: %v", err)
	}
	log.Printf("loaded %d account(s) from %s", len(auths), cfg.AuthDir)

	// redisstore：未配置/连接失败 → Noop（纯内存模式，一切功能照常）。
	store := redisstore.New(cfg.Upstash.URL, cfg.Upstash.Token)

	p := pool.New(cfg.StateFile)
	defer p.Flush() // 进程退出前强制落盘（后台 flush 每 5s 一次，退出时补一次）
	p.SetStore(store)
	p.RestoreFromSnapshot() // 择新恢复：Redis 快照比本地新才采用，否则本地优先
	p.SyncToDir(auths)      // 与 auths 目录对齐：新账号加入、已删除文件账号剔除（状态保留）

	// 熔断器 + 在途上限 + 三因子加权调优（从 config 注入，非正值回退默认）。
	p.SetBreaker(cfg.Pool.BreakerThreshold, cfg.BreakerCooldownDur, cfg.BreakerCooldownMaxD)
	p.SetMaxInFlight(cfg.Pool.MaxInFlight)
	p.SetSoftRateMax(cfg.SoftRateMaxDur) // 软冷却指数退避封顶（soft_rate_max，默认 2h）
	p.SetWeights(cfg.Pool.IdleWeightPerHour, cfg.Pool.IdleWeightMax)

	// 会话粘性路由（可配关闭）。
	var sessRouter *session.Router
	redisMode := "noop"
	if _, ok := store.(redisstore.Noop); !ok {
		redisMode = "upstash"
	}
	if cfg.SessionSticky.Enabled {
		sessRouter = session.New(session.Config{
			TTL:        cfg.SessionTTL,
			GCInterval: cfg.SessionGCInterval,
			Store:      store,
			Available:  p.AvailableUIDs,
		})
		sessRouter.LoadFromStore() // 启动时从 Redis 恢复粘性（读操作仅此处）
		sessRouter.StartGC()
		defer sessRouter.StopGC()
	}
	sessCount := func() int {
		if sessRouter != nil {
			return sessRouter.Count()
		}
		return 0
	}

	up := upstream.New()
	// 短 RPC 总时长上限（refresh/checkin/balance/FetchModels），语义不变。
	up.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	// 聊天 SSE 首字节前（响应头）上限：cfg 已 normalize（缺省回落 timeout_seconds）。
	up.HeaderTimeout = time.Duration(cfg.Upstream.HeaderTimeoutSeconds) * time.Second
	if tr, ok := up.ChatHTTP.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = up.HeaderTimeout
	}
	// 聊天 SSE 流中空闲上限（S3 空闲监控读取）。
	up.IdleTimeout = time.Duration(cfg.Upstream.IdleTimeoutSeconds) * time.Second
	up.SanitizeFingerprints = cfg.Features.SanitizeBlacklistFingerprints
	// 出站 UA 覆盖（issue #42）：非空才改写，空 = 现状 clientUA（指纹净化考虑）。
	up.UserAgent = cfg.Upstream.UserAgent

	sch := scheduler.New(scheduler.Config{
		Pool:                p,
		Upstream:            up,
		CheckinHours:        cfg.Schedule.CheckinHours,
		TravelHours:         cfg.Schedule.TravelHours,
		ActivityHours:       cfg.Schedule.ActivityHours,
		KeepaliveHours:      cfg.Schedule.KeepaliveHours,
		ActivityReportCount: cfg.Schedule.ActivityReportCount,
		CheckinDisabled:     !cfg.Schedule.CheckinEnabled,
		TravelDisabled:      !cfg.Schedule.TravelEnabled,
		ActivityDisabled:    !cfg.Schedule.ActivityEnabled,
		KeepaliveDisabled:   !cfg.Schedule.KeepaliveEnabled,
	})
	switch {
	case !cfg.Schedule.CheckinEnabled:
		log.Printf("签到已禁用（schedule.checkin_enabled=false）")
	default:
		log.Printf("签到已启用：%v 点（签到 + 余额查询解冻）", cfg.Schedule.CheckinHours)
	}
	switch {
	case !cfg.Schedule.TravelEnabled:
		log.Printf("猫猫旅行已禁用（schedule.travel_enabled=false）")
	default:
		log.Printf("猫猫旅行已启用：%v 点（独立排程：领养 / 派出 / 领奖）", cfg.Schedule.TravelHours)
	}
	switch {
	case !cfg.Schedule.ActivityEnabled:
		log.Printf("活跃上报已禁用（schedule.activity_enabled=false）")
	default:
		log.Printf("活跃上报已启用：%v 点（每号 %d 条，点亮连登 + 补满领猫对话门槛）", cfg.Schedule.ActivityHours, cfg.Schedule.ActivityReportCount)
	}
	if !cfg.Schedule.KeepaliveEnabled {
		log.Printf("token 保活已禁用（schedule.keepalive_enabled=false）")
	} else {
		log.Printf("token 保活已启用：%v 点", cfg.Schedule.KeepaliveHours)
	}

	// ── Web 管理界面（/admin）────────────────────────────────────────────
	// 配置读/写/重启以函数依赖注入 internal/admin：该包不依赖本文件的 Config 类型。
	absCfgPath, _ := filepath.Abs(*cfgPath)
	cwd, _ := os.Getwd()
	startedAt := time.Now()

	readConfigFile := func() (map[string]any, error) {
		raw, err := os.ReadFile(absCfgPath)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // 保留整数原貌，避免重写时 8 变成 8.0
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			return nil, err
		}
		return obj, nil
	}
	saveConfigFile := func(obj map[string]any) error {
		raw, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		// 语义校验：复用启动同一套加载管线（时长/小时范围/提示词文件都在 normalize 里校验），
		// 校验不过就原样报错、不动磁盘上的配置。
		validatePath := absCfgPath + ".validate.tmp"
		if err := os.WriteFile(validatePath, raw, 0o600); err != nil {
			return err
		}
		_, verr := Load(validatePath)
		_ = os.Remove(validatePath)
		if verr != nil {
			return verr
		}
		// 备份旧文件（含密钥，.gitignore 已排除 *.bak），再 tmp + rename 原子替换。
		if old, rerr := os.ReadFile(absCfgPath); rerr == nil {
			_ = os.WriteFile(absCfgPath+".bak", old, 0o600)
		}
		tmp := absCfgPath + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, absCfgPath)
	}
	effectiveConfig := func() map[string]any {
		return map[string]any{
			"config_path":            absCfgPath,
			"listen":                 cfg.Listen,
			"api_key_set":            cfg.APIKey != "",
			"auth_dir":               cfg.AuthDir,
			"state_file":             cfg.StateFile,
			"accounts":               len(auths),
			"redis_mode":             redisMode,
			"max_body_mb":            cfg.Server.MaxBodyMB,
			"soft_rate":              cfg.SoftRateDur.String(),
			"soft_rate_max":          cfg.SoftRateMaxDur.String(),
			"breaker_threshold":      cfg.Pool.BreakerThreshold,
			"max_in_flight":          cfg.Pool.MaxInFlight,
			"session_sticky":         cfg.SessionSticky.Enabled,
			"prompt_mode":            cfg.Prompt.Mode,
			"prompt_file":            cfg.Prompt.File,
			"user_agent":             cfg.Upstream.UserAgent,
			"timeout_seconds":        cfg.Upstream.TimeoutSeconds,
			"header_timeout_seconds": cfg.Upstream.HeaderTimeoutSeconds,
			"idle_timeout_seconds":   cfg.Upstream.IdleTimeoutSeconds,
			"schedule": map[string]any{
				"checkin_enabled":   cfg.Schedule.CheckinEnabled,
				"checkin_hours":     cfg.Schedule.CheckinHours,
				"travel_enabled":    cfg.Schedule.TravelEnabled,
				"travel_hours":      cfg.Schedule.TravelHours,
				"activity_enabled":  cfg.Schedule.ActivityEnabled,
				"activity_hours":    cfg.Schedule.ActivityHours,
				"keepalive_enabled": cfg.Schedule.KeepaliveEnabled,
				"keepalive_hours":   cfg.Schedule.KeepaliveHours,
			},
		}
	}

	// restartFn 在 srv 就绪后赋值；admin 侧通过闭包读取，避免构造顺序问题。
	var restartFn func()
	requestLogs := requestlog.New()
	adminHandler := admin.New(admin.Deps{
		RequestLogs: requestLogs,
		PricingFile: filepath.Join(filepath.Dir(absCfgPath), "pricing.json"),
		Pool:        p,
		Upstream:    up,
		APIKey:      cfg.APIKey,
		AuthDir:     cfg.AuthDir,
		ConfigPath:  absCfgPath,
		ConfigFile:  readConfigFile,
		SaveConfig:  saveConfigFile,
		Effective:   effectiveConfig,
		Restart: func() {
			if restartFn != nil {
				restartFn()
			}
		},
		StartedAt: startedAt,
	})
	log.Printf("Web 管理界面已启用：http://127.0.0.1%s/admin/", cfg.Listen)

	h := server.NewHandler(server.Config{
		RequestLogs:  requestLogs,
		Pool:         p,
		Upstream:     up,
		APIKey:       cfg.APIKey,
		Session:      sessRouter,
		StickyCount:  sessCount,
		RedisMode:    redisMode,
		SoftCooldown: cfg.SoftRateDur,
		PromptMode:   cfg.Prompt.Mode,
		PromptText:   cfg.PromptText,
		MaxBodyBytes: int64(cfg.Server.MaxBodyMB) << 20, // MB → 字节
		Admin:        adminHandler,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sch.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
	}
	// srv 就绪后绑定重启实现（/admin 的「保存并重启」用它重新拉起进程）。
	restartFn = func() { restartSelf(srv, p, cwd) }
	go func() {
		<-ctx.Done()
		p.Flush() // 信号触发：先落盘再做优雅停机
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("workbuddy2api listening on %s (api_key=%v)", cfg.Listen, cfg.APIKey != "")
	ln, err := listenWithRetry(cfg.Listen)
	if err != nil {
		log.Fatalf("http: %v", err)
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http: %v", err)
	}
	log.Printf("bye")
}

// restartSelf 重新拉起自身（Web 管理界面「保存并重启」的落地实现）。
// 新进程继承当前 stdout/stderr（即启动时重定向的日志文件）与工作目录、参数。
//
// 顺序很关键：**先拉起新进程，再停机释放端口**。
// 反过来的话（先 Shutdown 再 Start），Serve 返回后 main 会立刻走完并退出进程，
// 把还在 Start 之前的重启 goroutine 一起带走，新进程根本没机会创建（实测如此）。
// 让新进程先启动 + listenWithRetry 重试，端口一释放它就自动接管，无竞态。
func restartSelf(srv *http.Server, p *pool.Pool, cwd string) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("ERR: [admin] restart: resolve executable: %v", err)
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		log.Printf("ERR: [admin] restart spawn: %v（已放弃重启，服务继续运行）", err)
		return
	}
	log.Printf("[admin] 新进程已启动 pid=%d（等待端口释放后接管），旧进程停机中", cmd.Process.Pid)
	p.Flush()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(shutdownCtx)
	cancel()
}

// listenWithRetry 绑定监听地址；端口被占用时短暂重试若干次再放弃。
// 存在的意义：/admin 的「重启」语义是「旧进程停机 → 拉起新进程」，Windows 上旧监听
// socket 残留的 TIME_WAIT 可能让新进程首次 bind 失败，重试几秒即可平滑接管。
// 正常情况下（端口空闲）第一次即成功，不引入任何额外延迟。
func listenWithRetry(addr string) (net.Listener, error) {
	var lastErr error
	for i := 0; i < 20; i++ {
		var ln net.Listener
		ln, lastErr = net.Listen("tcp", addr)
		if lastErr == nil {
			return ln, nil
		}
		if i == 0 {
			log.Printf("WARN: 监听 %s 失败（%v），持续重试…", addr, lastErr)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil, lastErr
}
