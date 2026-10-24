package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"qoderwake-panel/internal/gateway"
	"qoderwake-panel/internal/server"
	"strconv"
	"syscall"
)

func gatewayCommand(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return gatewayControl(args)
	}
	defaults := server.DefaultConfig()
	c := gateway.RunConfig{Root: defaults.Root, Upstream: env("QW_GW_UPSTREAM", "openapi.qoder.sh"), Port: 19840, ConfigPath: os.Getenv("QW_GW_CONFIG"), Nonce: os.Getenv("QW_GW_NONCE"), ExpectedConfigHash: os.Getenv("QW_GW_CONFIG_HASH"), ExpectedBinaryHash: os.Getenv("QW_GW_BINARY_HASH")}
	if port := os.Getenv("QW_GW_PORT"); port != "" {
		var err error
		c.Port, err = strconv.Atoi(port)
		if err != nil {
			return errors.New("invalid_gateway_port")
		}
	}
	flags := flag.NewFlagSet("panel gateway serve", flag.ContinueOnError)
	flags.StringVar(&c.Root, "root", c.Root, "Panel 数据目录")
	flags.StringVar(&c.ConfigPath, "config", c.ConfigPath, "已保存的网关策略 JSON")
	flags.IntVar(&c.Port, "port", c.Port, "回环监听端口")
	flags.StringVar(&c.Upstream, "upstream", c.Upstream, "openapi.qoder.sh 或 openapi.qoder.com.cn")
	flags.BoolVar(&c.Check, "check", false, "检查策略、可执行文件与日志权限后退出")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("无效的网关参数")
	}
	var err error
	c.Root, err = filepath.Abs(c.Root)
	if err != nil {
		return err
	}
	if root, e := filepath.EvalSymlinks(c.Root); e == nil {
		c.Root = root
	}
	if c.ConfigPath == "" {
		c.ConfigPath = filepath.Join(c.Root, "config", "uplink-gw.json")
	}
	if c.Nonce == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		c.Nonce = hex.EncodeToString(raw)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return gateway.Run(ctx, c, os.Stdout)
}
