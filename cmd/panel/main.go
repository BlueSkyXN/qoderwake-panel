// Command panel runs the self-hosted collaboration workspace.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/server"
	"qoderwake-panel/internal/webui"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func password(stdin bool) (string, error) {
	if stdin {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 8192)
		if !scanner.Scan() {
			return "", errors.New("未读取到密码")
		}
		return scanner.Text(), scanner.Err()
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("请使用交互终端，或通过 --password-stdin 读取密码")
	}
	fmt.Fprint(os.Stderr, "密码：")
	value, e := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if e != nil {
		return "", e
	}
	fmt.Fprint(os.Stderr, "再次输入：")
	again, e := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if e != nil {
		return "", e
	}
	if string(value) != string(again) {
		return "", errors.New("两次密码不一致")
	}
	return string(value), nil
}
func run() error {
	cfg := server.DefaultConfig()
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command == "daemon" {
		return daemonCommand(args)
	}
	if command == "service-config" {
		return serviceCommand(args)
	}
	if command == "gateway" {
		return gatewayCommand(args)
	}
	flags := flag.NewFlagSet("panel "+command, flag.ContinueOnError)
	flags.StringVar(&cfg.Root, "root", cfg.Root, "Panel 数据目录")
	if command == "init-admin" || command == "reset-password" {
		username := flags.String("username", "", "用户名")
		name := flags.String("name", "管理员", "显示名称")
		stdin := flags.Bool("password-stdin", false, "从标准输入读取一行密码")
		if e := flags.Parse(args); e != nil {
			return e
		}
		if *username == "" {
			return errors.New("请指定 --username")
		}
		store, e := auth.Open(cfg.Root)
		if e != nil {
			return e
		}
		defer store.Close()
		value, e := password(*stdin)
		if e != nil {
			return e
		}
		if command == "init-admin" {
			_, e = store.CreateUser(*username, *name, "admin", nil, value, true)
		} else {
			var id string
			e = store.DB.QueryRow("SELECT id FROM users WHERE username=?", *username).Scan(&id)
			if e == nil {
				e = store.ResetPassword(id, value, nil)
			}
		}
		if e == nil {
			fmt.Println("账号设置已保存。")
		}
		return e
	}
	if command != "serve" {
		return errors.New("支持的命令：serve、init-admin、reset-password、daemon start|status|stop、gateway serve、service-config")
	}
	flags.StringVar(&cfg.Home, "home", cfg.Home, "QoderWake 数据目录")
	flags.StringVar(&cfg.DaemonURL, "daemon-url", cfg.DaemonURL, "QoderWake HTTP 地址")
	flags.StringVar(&cfg.CLI, "daemon-bin", cfg.CLI, "官方 CLI 路径")
	bind := flags.String("bind", env("QW_BIND", "127.0.0.1"), "监听地址")
	port := flags.Int("port", 19831, "监听端口")
	legacy := flags.Bool("legacy", false, "允许旧共享令牌模式")
	if p, e := strconv.Atoi(os.Getenv("QW_PORT")); e == nil {
		*port = p
	}
	if e := flags.Parse(args); e != nil {
		return e
	}
	if *port < 0 || *port > 65535 {
		return errors.New("无效端口")
	}
	if e := os.MkdirAll(cfg.Root, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(cfg.Root, "panel-service.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("该数据目录已有 Panel 运行")
	}
	if e = lock.Truncate(0); e != nil {
		return e
	}
	fmt.Fprintln(lock, os.Getpid())
	cfg.Assets = webui.Handler()
	app, e := server.New(cfg)
	if e != nil {
		return e
	}
	defer app.Close()
	if !*legacy && !app.Auth.AccountsEnabled() {
		return errors.New("请先运行 panel init-admin 初始化管理员")
	}
	listener, e := net.Listen("tcp", net.JoinHostPort(*bind, strconv.Itoa(*port)))
	if e != nil {
		return e
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: app, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 * 1024}
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-stop.Done()
		ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		httpServer.Shutdown(ctx)
	}()
	cert, key := os.Getenv("QW_TLS_CERT"), os.Getenv("QW_TLS_KEY")
	if (cert == "") != (key == "") {
		return errors.New("请同时配置 QW_TLS_CERT 与 QW_TLS_KEY")
	}
	scheme := "http"
	if cert != "" {
		scheme = "https"
	}
	fmt.Printf("Panel %s · %s://%s\n", server.Version, scheme, listener.Addr())
	if cert != "" {
		e = httpServer.ServeTLS(listener, cert, key)
	} else {
		e = httpServer.Serve(listener)
	}
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
