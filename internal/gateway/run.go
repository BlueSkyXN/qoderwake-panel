package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const ProtocolVersion = 2

type RunConfig struct {
	Root, ConfigPath, Upstream, Nonce, ExpectedConfigHash, ExpectedBinaryHash string
	Port                                                                      int
	Check                                                                     bool
}

func regular(path string, limit int64) ([]byte, error) {
	dir, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := openAt(dir, filepath.Base(path), unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("gateway_file_invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("gateway_file_invalid")
	}
	return raw, nil
}
func fileHash(path string) (string, error) {
	dir, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer dir.Close()
	f, err := openAt(dir, filepath.Base(path), unix.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024*1024 {
		return "", errors.New("gateway_file_invalid")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(f, 1024*1024*1024+1))
	if err != nil || size != info.Size() {
		return "", errors.New("gateway_file_invalid")
	}
	after, err := f.Stat()
	if err != nil || !after.ModTime().Equal(info.ModTime()) || after.Size() != info.Size() {
		return "", errors.New("gateway_file_changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func ReadPolicy(path string) (Policy, error) {
	raw, err := regular(path, 1024*1024)
	if err != nil {
		return Policy{}, err
	}
	return DecodePolicy(raw)
}
func Run(ctx context.Context, c RunConfig, out io.Writer) error {
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("invalid_gateway_port")
	}
	if !ApprovedUpstream(c.Upstream) {
		return errors.New("invalid_gateway_upstream")
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(c.Nonce) {
		return errors.New("invalid_gateway_nonce")
	}
	var err error
	c.Root, err = filepath.EvalSymlinks(c.Root)
	if err != nil {
		return errors.New("gateway_root_unavailable")
	}
	c.Root, err = filepath.Abs(c.Root)
	if err != nil {
		return err
	}
	c.ConfigPath, err = filepath.Abs(c.ConfigPath)
	if err != nil {
		return err
	}
	p, err := ReadPolicy(c.ConfigPath)
	if err != nil {
		return err
	}
	if c.ExpectedConfigHash != "" && p.Hash() != c.ExpectedConfigHash {
		return errors.New("gateway_config_hash_mismatch")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	binaryHash, err := fileHash(executable)
	if err != nil {
		return err
	}
	if c.ExpectedBinaryHash != "" && binaryHash != c.ExpectedBinaryHash {
		return errors.New("gateway_binary_hash_mismatch")
	}
	logger := NewLogger(c.Root)
	if err = logger.Prepare(); err != nil {
		return err
	}
	health := map[string]any{"schemaVersion": ProtocolVersion, "service": "qw-control-gateway", "pid": os.Getpid(), "nonce": c.Nonce, "binaryHash": binaryHash, "configHash": p.Hash(), "mode": p.Mode, "port": c.Port, "root": c.Root, "upstream": c.Upstream, "configPath": c.ConfigPath}
	if c.Check {
		return json.NewEncoder(out).Encode(map[string]any{"ok": true, "schemaVersion": ProtocolVersion, "binaryHash": binaryHash, "configHash": p.Hash(), "mode": p.Mode, "port": c.Port, "root": c.Root, "upstream": c.Upstream})
	}
	handler, err := NewHandler(p, c.Upstream, c.Port, health, logger.Append)
	if err != nil {
		return err
	}
	defer handler.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 64 * 1024}
	stopped := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if server.Shutdown(timeout) != nil {
				server.Close()
			}
		case <-stopped:
		}
	}()
	fmt.Fprintf(out, "control-gateway mode=%s port=%d\n", p.Mode, c.Port)
	err = server.Serve(listener)
	close(stopped)
	<-watchDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
