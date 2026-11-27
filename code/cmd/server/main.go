// 飞牛AT力场 无头服务端：监听 unix socket 与本地 TCP，
// 提供 Web 界面与 REST 接口，用于 fnOS 统一网关接入。
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"atforcefield/frontend"
	"atforcefield/internal/alert"
	"atforcefield/internal/api"
	"atforcefield/internal/audit"
	"atforcefield/internal/config"
	"atforcefield/internal/metrics"
	"atforcefield/internal/secwatch"
	"atforcefield/internal/store"
	"atforcefield/internal/weblog"
)

var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[atforcefield] ")
	log.SetOutput(os.Stderr)

	st, err := store.Open()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 真实指标采集：后台每 30 秒采样一次，持久化 7 天用于趋势图
	coll, err := metrics.Open(config.DataDir())
	if err != nil {
		log.Fatalf("初始化指标采集失败: %v", err)
	}
	defer coll.Close()
	coll.Start()

	// 操作审计日志（启用/关闭防护、修改配置等真实事件）
	au, err := audit.Open(config.DataDir())
	if err != nil {
		log.Fatalf("初始化审计日志失败: %v", err)
	}
	defer au.Close()
	au.Add("info", "系统", "飞牛AT力场服务已启动（v"+Version+"），开始采集真实系统指标")

	// 阈值告警：每次采样后评估健康/磁盘/内存/温度，触发时写审计日志并可推送飞书。
	// 去重时间戳落盘到数据目录，应用重启后不会重复推送同一条告警。
	evaluator := alert.New(au, st.Config, config.DataDir()+"/alert_state.json")
	coll.SetOnSample(evaluator.Evaluate)

	// 安全巡检：SSH 暴力破解、防火墙、fail2ban、SMART、SSH 配置，事件写入审计日志
	sec := secwatch.NewWatcher(au)
	sec.Start()

	// Web 访问日志采集：增量跟随 nginx access.log，按小时聚合，保留 30 天
	web, err := weblog.Open(config.DataDir(), config.BasePath())
	if err != nil {
		log.Printf("初始化访问日志采集失败，访问分析不可用: %v", err)
	} else {
		web.Start()
		defer web.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	api.Version = Version
	handler := api.New(st, coll, au, sec, evaluator, web, frontend.Dist(), config.BasePath())
	handler.StartAutoBan()
	handler.StartSecurityAlerts()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second}

	listeners := make([]net.Listener, 0, 2)
	if ln, err := net.Listen("tcp", config.Addr()); err != nil {
		log.Printf("TCP 监听失败 %s: %v", config.Addr(), err)
	} else {
		log.Printf("HTTP 监听: http://%s", ln.Addr())
		listeners = append(listeners, ln)
	}
	if runtime.GOOS != "windows" {
		if ln, err := listenUnix(config.SockPath()); err != nil {
			log.Printf("Unix socket 监听失败 %s: %v", config.SockPath(), err)
		} else {
			log.Printf("Unix socket 监听: %s", config.SockPath())
			listeners = append(listeners, ln)
		}
	}
	if len(listeners) == 0 {
		log.Fatal("没有可用监听地址，退出")
	}

	for _, ln := range listeners {
		go func(ln net.Listener) {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("服务异常: %v", err)
			}
		}(ln)
	}

	log.Printf("飞牛AT力场 v%s 启动完成", Version)

	<-ctx.Done()
	log.Print("收到退出信号，正在清理")
	au.Add("warn", "系统", "飞牛AT力场服务已停止")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = os.Remove(config.SockPath())
}

// listenUnix 重建 socket 文件并放开权限，便于统一网关以其他用户访问
func listenUnix(path string) (net.Listener, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}
