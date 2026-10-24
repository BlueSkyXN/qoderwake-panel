// Package service renders native user-service configuration for a standalone Panel binary.
package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Config struct {
	Platform, Executable, Root, Home, Daemon, URL, Bind, Label string
	Port                                                       int
	Environment                                                map[string]string
	Arguments                                                  []string
	LogPrefix                                                  string
	DisableKeepAlive                                           bool
}

var labelPattern = regexp.MustCompile(`^com\.qoderwake\.panel\.[a-z0-9.-]{1,90}$`)

func (c Config) Name() string {
	if c.Label != "" {
		return c.Label
	}
	sum := sha256.Sum256([]byte(c.Root))
	return "com.qoderwake.panel." + hex.EncodeToString(sum[:8])
}
func (c Config) arguments() []string {
	if len(c.Arguments) > 0 {
		return append([]string(nil), c.Arguments...)
	}
	args := []string{c.Executable, "serve", "--root", c.Root, "--home", c.Home, "--bind", c.Bind, "--port", strconv.Itoa(c.Port)}
	if c.Daemon != "" {
		args = append(args, "--daemon-bin", c.Daemon)
	}
	if c.URL != "" {
		args = append(args, "--daemon-url", c.URL)
	}
	return args
}
func (c Config) Render() ([]byte, error) {
	if c.LogPrefix == "" {
		c.LogPrefix = "panel"
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`).MatchString(c.LogPrefix) || c.arguments()[0] != c.Executable {
		return nil, errors.New("invalid_service_configuration")
	}
	if !filepath.IsAbs(c.Executable) || !filepath.IsAbs(c.Root) || !filepath.IsAbs(c.Home) || !labelPattern.MatchString(c.Name()) || c.Port < 1 || c.Port > 65535 || net.ParseIP(c.Bind) == nil {
		return nil, errors.New("invalid_service_configuration")
	}
	for _, value := range append(c.arguments(), c.Name()) {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid_service_configuration")
		}
	}
	env := map[string]string{}
	for key, value := range c.Environment {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid_service_environment")
		}
		env[key] = value
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	if c.Platform == "darwin" {
		out.WriteString(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>`)
		text := func(tag, value string) {
			out.WriteString("<" + tag + ">")
			xml.EscapeText(&out, []byte(value))
			out.WriteString("</" + tag + ">")
		}
		pair := func(key, value string) { text("key", key); text("string", value) }
		pair("Label", c.Name())
		text("key", "ProgramArguments")
		out.WriteString("<array>")
		for _, arg := range c.arguments() {
			text("string", arg)
		}
		out.WriteString("</array>")
		pair("WorkingDirectory", c.Root)
		pair("StandardOutPath", filepath.Join(c.Root, "logs", c.LogPrefix+".stdout.log"))
		pair("StandardErrorPath", filepath.Join(c.Root, "logs", c.LogPrefix+".stderr.log"))
		text("key", "RunAtLoad")
		out.WriteString("<true/>")
		text("key", "KeepAlive")
		if c.DisableKeepAlive {
			out.WriteString("<false/>")
		} else {
			out.WriteString("<dict><key>SuccessfulExit</key><false/></dict>")
		}
		text("key", "ThrottleInterval")
		out.WriteString("<integer>5</integer>")
		pair("ProcessType", "Background")
		text("key", "EnvironmentVariables")
		out.WriteString("<dict>")
		for _, key := range keys {
			pair(key, env[key])
		}
		out.WriteString("</dict></dict></plist>\n")
	} else if c.Platform == "linux" {
		quote := func(value string, argument bool) string {
			value = strings.ReplaceAll(value, "\\", "\\\\")
			value = strings.ReplaceAll(value, "\"", "\\\"")
			value = strings.ReplaceAll(value, "%", "%%")
			if argument {
				value = strings.ReplaceAll(value, "$", "$$")
			}
			return "\"" + value + "\""
		}
		out.WriteString("[Unit]\nDescription=QoderWake Panel\nAfter=network.target\n\n[Service]\nType=simple\n")
		// WorkingDirectory 接收原始路径，不解析 argv 引号；末尾 /. 保留路径中的尾随空白或反斜杠。
		fmt.Fprintf(&out, "WorkingDirectory=%s/.\nExecStart=", strings.ReplaceAll(c.Root, "%", "%%"))
		for i, arg := range c.arguments() {
			if i > 0 {
				out.WriteByte(' ')
			}
			out.WriteString(quote(arg, true))
		}
		out.WriteByte('\n')
		for _, key := range keys {
			fmt.Fprintf(&out, "Environment=%s\n", quote(key+"="+env[key], false))
		}
		if c.DisableKeepAlive {
			out.WriteString("Restart=no\n")
		} else {
			out.WriteString("Restart=on-failure\n")
		}
		out.WriteString("RestartSec=5\nTimeoutStopSec=180\nUMask=0077\n\n[Install]\nWantedBy=default.target\n")
	} else {
		return nil, errors.New("unsupported_service_platform")
	}
	return out.Bytes(), nil
}
func Write(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		os.Remove(path)
		return err
	}
	return closeErr
}
