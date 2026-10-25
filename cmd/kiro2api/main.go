// kiro2api：Anthropic Messages 兼容网关，后端是 Kiro 账号池和 Claude Messages API 账号。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	adminui "github.com/FoyonaCZY/kiro2api/admin-ui"
	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/admin"
	"github.com/FoyonaCZY/kiro2api/internal/anthropic"
	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/FoyonaCZY/kiro2api/internal/usage"
)

var version = "dev"

func main() {
	var cfgPath, credPath string
	var showVersion bool
	flag.StringVar(&cfgPath, "c", "config.json", "配置文件路径")
	flag.StringVar(&cfgPath, "config", "config.json", "配置文件路径")
	flag.StringVar(&credPath, "credentials", "credentials.json", "凭证文件路径")
	flag.BoolVar(&showVersion, "version", false, "显示版本")
	flag.Parse()
	if showVersion {
		fmt.Println("kiro2api", version)
		return
	}
	level := slog.LevelInfo
	if strings.Contains(strings.ToLower(os.Getenv("RUST_LOG")+os.Getenv("LOG_LEVEL")), "debug") {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	if err := run(cfgPath, credPath); err != nil {
		slog.Error("启动失败", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath, credPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	creds, err := kiro.LoadStartupCredentials(credPath)
	if err != nil {
		return fmt.Errorf("加载凭证失败: %w", err)
	}
	if key, ok := os.LookupEnv("KIRO_API_KEY"); ok {
		if key == "" {
			slog.Warn("KIRO_API_KEY 环境变量已设置但为空，视为未配置")
		} else {
			slog.Info("检测到 KIRO_API_KEY 环境变量，添加 API Key 凭据（最高优先级）")
			creds = append([]*kiro.Credentials{{KiroAPIKey: key, AuthMethod: "api_key"}}, creds...)
		}
	}
	slog.Info("已加载凭据配置", "count", len(creds))
	if cfg.APIKey == "" {
		return errors.New("配置文件中未设置 apiKey")
	}
	var proxy *httpx.Proxy
	if cfg.ProxyURL != "" {
		proxy = &httpx.Proxy{URL: cfg.ProxyURL}
		if cfg.ProxyUsername != "" && cfg.ProxyPassword != "" {
			proxy.Username, proxy.Password = cfg.ProxyUsername, cfg.ProxyPassword
		}
		slog.Info("已配置全局代理", "url", kiro.DisplayProxyURL(cfg.ProxyURL))
	}
	eps := kiro.Endpoints()
	if _, ok := eps[cfg.DefaultEndpoint]; !ok {
		return fmt.Errorf("默认端点 %q 未注册", cfg.DefaultEndpoint)
	}
	for _, c := range creds {
		if c.IsClaudeAPI() {
			continue
		}
		if name := kiro.EffectiveEndpointName(c.Endpoint, cfg.DefaultEndpoint, c.IsAPIKey()); eps[name] == nil {
			return fmt.Errorf("凭据 id=%d 指定了未知端点 %q", c.ID, name)
		}
	}
	tm, err := kiro.NewManager(cfg, creds, proxy, credPath)
	if err != nil {
		return fmt.Errorf("创建 Token 管理器失败: %w", err)
	}
	defer tm.FlushStats()
	provider := kiro.NewProvider(tm, eps, cfg.DefaultEndpoint)
	anthropic.InitCountTokens(anthropic.CountTokensConfig{APIURL: cfg.CountTokensAPIURL, APIKey: cfg.CountTokensAPIKey, AuthType: cfg.CountTokensAuthType, Proxy: tm.GlobalProxy})
	if cfg.CacheEmulation.Enabled {
		slog.Info("已开启模拟缓存计费", "read_ratio", cfg.CacheEmulation.ReadRatio, "creation_ratio", cfg.CacheEmulation.CreationRatio)
	}
	anthropic.InitCache(cfg.CacheEmulation)

	dir := tm.Dir()
	ul := usage.OpenLimited(filepath.Join(dir, "usage.sqlite"), cfg.UsageMaxBytes())
	acc := access.Open(filepath.Join(dir, "access.sqlite"))
	acc.ImportInitialKey(cfg.APIKey)

	mux := http.NewServeMux()
	api := &anthropic.Server{APIKey: cfg.APIKey, Provider: provider, ExtractThinking: cfg.ExtractThinking, Usage: ul, Access: acc}
	api.Routes(mux)
	var svc *admin.Service
	if strings.TrimSpace(cfg.AdminAPIKey) == "" {
		slog.Warn("adminApiKey 未配置或为空，Admin API 未启用")
	} else {
		svc = admin.NewService(cfg.AdminAPIKey, tm, eps, ul, acc)
		svc.BackfillAccountUsage()
		svc.Routes(mux)
		ui := adminui.Handler()
		mux.Handle("GET /admin", ui)
		mux.Handle("GET /admin/", ui)
		slog.Info("Admin API 已启用，Admin UI: /admin")
	}

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS 只给 Anthropic 接口，与 kiro.rs 相同。
		if strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/cc/v1/") {
			anthropic.CORS(mux).ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	addr := cfg.Host + ":" + strconv.Itoa(cfg.Port)
	srv := &http.Server{Addr: addr, Handler: root, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	slog.Info("启动 Anthropic API 端点", "addr", addr, "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("收到退出信号，正在关闭")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
	if svc != nil {
		svc.SaveBalanceCache()
	}
	return nil
}
