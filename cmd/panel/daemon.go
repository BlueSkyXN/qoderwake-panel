package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qoderwake-panel/internal/managed"
	"qoderwake-panel/internal/server"
)

// daemonCommand 仅管理本数据目录创建的实例；已有官方服务需保留原管理入口。
func daemonCommand(args []string) error {
	if len(args) == 0 || (args[0] != "start" && args[0] != "status" && args[0] != "stop") {
		return errors.New("用法：panel daemon start|status|stop --root <Panel 数据目录> --home <QoderWake 数据目录>")
	}
	action := args[0]
	defaults := server.DefaultConfig()
	cfg := managed.Config{Root: defaults.Root, Home: defaults.Home, Launch: defaults.CLI, Mode: "direct", Name: "daemon"}
	flags := flag.NewFlagSet("panel daemon "+action, flag.ContinueOnError)
	flags.StringVar(&cfg.Root, "root", cfg.Root, "Panel 数据目录")
	flags.StringVar(&cfg.Home, "home", cfg.Home, "QoderWake 数据目录")
	flags.StringVar(&cfg.Launch, "daemon-bin", cfg.Launch, "官方 CLI 路径")
	flags.StringVar(&cfg.Name, "name", cfg.Name, "受管实例名称（daemon 或旧 daemon-cn）")
	flags.IntVar(&cfg.Port, "port", 0, "daemon 监听端口；默认读取 home/run/daemon.port 或使用 19820")
	flags.StringVar(&cfg.Mode, "mode", cfg.Mode, "direct 或 gateway")
	flags.StringVar(&cfg.Endpoint, "gateway-endpoint", "", "gateway 模式的本机回环地址")
	flags.StringVar(&cfg.ExpectedVersion, "expected-version", "", "启动后预期健康检查版本")
	flags.StringVar(&cfg.HealthURL, "health-url", "", "默认使用该端口的本机 HTTP /api/health")
	flags.StringVar(&cfg.CAFile, "health-ca", "", "HTTPS 健康检查 CA 文件")
	flags.StringVar(&cfg.ServerName, "health-server-name", "", "HTTPS 健康检查服务名称")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || (cfg.Name != "daemon" && cfg.Name != "daemon-cn") {
		return errors.New("无效的 daemon 参数")
	}
	provided := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	previous, err := managed.Read(filepath.Join(cfg.Root, "process-state", cfg.Name+".json"))
	if err != nil {
		return err
	}
	base := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (managed.DaemonKeys[key] || managed.ProxyKeys[key]) {
			base[key] = value
		}
	}
	if previous != nil {
		if !provided["home"] {
			cfg.Home = previous.Home
		}
		if !provided["daemon-bin"] {
			cfg.Launch = previous.Launch.Path
		}
		if !provided["port"] {
			cfg.Port = previous.Port
		}
		if !provided["mode"] {
			cfg.Mode = previous.Mode
		}
		if !provided["gateway-endpoint"] && previous.Endpoint != nil {
			cfg.Endpoint = *previous.Endpoint
		}
		for key, value := range previous.Environment {
			base[key] = value
		}
	}
	if cfg.Launch == "" {
		cfg.Launch = filepath.Join(cfg.Home, "qoderwake")
	}
	if cfg.Port == 0 {
		cfg.Port = 19820
		if raw, err := os.ReadFile(filepath.Join(cfg.Home, "run", "daemon.port")); err == nil && len(raw) <= 32 {
			if value, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && value > 0 && value < 65536 {
				cfg.Port = value
			}
		}
	}
	if cfg.HealthURL == "" {
		cfg.HealthURL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)) + "/api/health"
	}
	policy, err := managed.ReadPolicy(cfg.Root)
	if err != nil {
		return err
	}
	cfg.Environment, err = policy.Environment(base)
	if err != nil {
		return err
	}
	controller, err := managed.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	var result map[string]any
	switch action {
	case "start":
		result, err = controller.Start(ctx)
	case "stop":
		result, err = controller.Stop(ctx)
	case "status":
		result, err = controller.Status(ctx)
	}
	if err != nil {
		return err
	}
	raw, err := json.Marshal(result)
	if err == nil {
		fmt.Println(string(raw))
	}
	return err
}
