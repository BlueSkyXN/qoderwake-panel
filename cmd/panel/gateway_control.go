package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"qoderwake-panel/internal/gateway"
	"qoderwake-panel/internal/server"
	"strconv"
	"time"
)

func gatewayControl(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：panel gateway serve|status|preflight|apply|rollback|recover|stop|gc")
	}
	action := args[0]
	if action != "status" && action != "preflight" && action != "apply" && action != "rollback" && action != "recover" && action != "stop" && action != "gc" {
		return errors.New("无效的网关操作")
	}
	defaults := server.DefaultConfig()
	root := defaults.Root
	config := os.Getenv("QW_GW_CONFIG")
	upstream := os.Getenv("QW_GW_UPSTREAM")
	account := os.Getenv("QW_GW_USER")
	port := 19840
	if raw := os.Getenv("QW_GW_PORT"); raw != "" {
		var err error
		port, err = strconv.Atoi(raw)
		if err != nil {
			return errors.New("invalid_gateway_port")
		}
	}
	flags := flag.NewFlagSet("panel gateway "+action, flag.ContinueOnError)
	flags.StringVar(&root, "root", root, "Panel 数据目录")
	flags.StringVar(&config, "config", config, "保存的网关策略；位于 root/config 下")
	flags.StringVar(&upstream, "upstream", upstream, "沿用已有实例，或选择 openapi.qoder.sh / openapi.qoder.com.cn")
	flags.StringVar(&account, "user", account, "网关系统账户；Mac 默认当前用户，Linux root 默认 qwgw")
	flags.IntVar(&port, "port", port, "网关回环监听端口")
	candidate := flags.String("candidate", "", "预检或应用的策略文件；默认读取已保存策略")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("无效的网关参数")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	control, err := gateway.NewControl(root, binary, config, upstream, account, port)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var result map[string]any
	switch action {
	case "status":
		result = control.Status(ctx)
	case "stop":
		err = control.Stop(ctx)
		result = map[string]any{"ok": err == nil}
	case "recover":
		if gateway.IsLegacy(control.Native.Root) {
			return errors.New("legacy_gateway_recovery_uses_apply_or_rollback")
		}
		err = control.Native.Recover(ctx)
		result = map[string]any{"ok": err == nil}
	case "gc":
		if gateway.IsLegacy(control.Native.Root) {
			return errors.New("legacy_gateway_gc_managed_by_transactions")
		}
		result, err = control.Native.GC(ctx)
	case "rollback":
		result, err = control.Rollback(ctx)
	default:
		source := *candidate
		if source == "" {
			source = control.Native.ConfigPath
		}
		source, err = filepath.Abs(source)
		if err != nil {
			return err
		}
		p, e := gateway.ReadPolicy(source)
		if e != nil {
			return e
		}
		if action == "preflight" {
			result, err = control.Preflight(ctx, p)
		} else {
			result, err = control.Apply(ctx, p)
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
