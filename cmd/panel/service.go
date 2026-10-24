package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"qoderwake-panel/internal/managed"
	"qoderwake-panel/internal/server"
	"qoderwake-panel/internal/service"
	"runtime"
	"strings"
)

func serviceCommand(args []string) error {
	defaults := server.DefaultConfig()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	c := service.Config{Platform: runtime.GOOS, Executable: executable, Root: defaults.Root, Home: defaults.Home, Daemon: defaults.CLI, URL: defaults.DaemonURL, Bind: "127.0.0.1", Port: 19831, Environment: map[string]string{}}
	flags := flag.NewFlagSet("panel service-config", flag.ContinueOnError)
	flags.StringVar(&c.Root, "root", c.Root, "Panel 数据目录")
	flags.StringVar(&c.Home, "home", c.Home, "QoderWake 数据目录")
	flags.StringVar(&c.Daemon, "daemon-bin", c.Daemon, "官方 CLI 路径")
	flags.StringVar(&c.URL, "daemon-url", c.URL, "QoderWake HTTP 地址")
	flags.StringVar(&c.Bind, "bind", c.Bind, "Panel 监听地址")
	flags.IntVar(&c.Port, "port", c.Port, "Panel 监听端口")
	flags.StringVar(&c.Label, "label", "", "服务名称，需以 com.qoderwake.panel. 开头")
	flags.StringVar(&c.Platform, "platform", c.Platform, "darwin 或 linux")
	output := flags.String("output", "", "新建配置文件路径；已有文件会被保留")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" || flags.NArg() != 0 {
		return errors.New("请指定 --output 配置文件路径")
	}
	c.Root, err = filepath.Abs(c.Root)
	if err != nil {
		return err
	}
	c.Home, err = filepath.Abs(c.Home)
	if err != nil {
		return err
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (managed.PanelKeys[key] || managed.ProxyKeys[key] || key == "QW_DAEMON_CREDENTIAL_SERVICE" || key == "QW_REQUIRED_DIRECT") {
			c.Environment[key] = value
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		c.Environment["HOME"] = home
	}
	c.Environment["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	c.Environment["LANG"] = "en_US.UTF-8"
	content, err := c.Render()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(c.Root, "logs"), 0700); err != nil {
		return err
	}
	if err = service.Write(*output, content); err != nil {
		return err
	}
	fmt.Printf("服务配置已写入：%s\n服务名称：%s\n", *output, c.Name())
	return nil
}
