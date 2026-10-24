package server

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	contracts "qoderwake-panel/panel/config"
)

type stateCache struct {
	mu, refresh sync.Mutex
	value       map[string]any
	at          time.Time
	epoch       uint64
}

func (s *Server) invalidateState() {
	s.cached.mu.Lock()
	s.cached.epoch++
	s.cached.at = time.Time{}
	s.cached.mu.Unlock()
}
func copyState(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for k, v := range value {
		result[k] = v
	}
	return result
}
func (s *Server) cachedState(ctx context.Context) (map[string]any, error) {
	s.cached.mu.Lock()
	if s.cached.value != nil && time.Since(s.cached.at) < 60*time.Second {
		value := copyState(s.cached.value)
		s.cached.mu.Unlock()
		return value, nil
	}
	s.cached.mu.Unlock()
	s.cached.refresh.Lock()
	defer s.cached.refresh.Unlock()
	for range 2 {
		s.cached.mu.Lock()
		if s.cached.value != nil && time.Since(s.cached.at) < 60*time.Second {
			value := copyState(s.cached.value)
			s.cached.mu.Unlock()
			return value, nil
		}
		epoch := s.cached.epoch
		s.cached.mu.Unlock()
		value, err := s.state(ctx, auth.Identity{Role: "admin"})
		if err != nil {
			return nil, err
		}
		s.cached.mu.Lock()
		if epoch == s.cached.epoch {
			s.cached.value = value
			s.cached.at = time.Now()
			s.cached.mu.Unlock()
			return copyState(value), nil
		}
		s.cached.mu.Unlock()
	}
	return nil, apierror.New(503, "state_changed_during_refresh")
}

var whitelistIP = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

func (s *Server) whitelist() []map[string]any {
	paths := []string{s.Config.Whitelist, filepath.Join(s.Config.Root, "config/whitelist.json")}
	if binary, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(binary), "config/whitelist.json"))
	}
	raw := contracts.Whitelist
	for _, path := range paths {
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		file.Close()
		if err == nil && len(data) <= 1024*1024 {
			var value map[string]any
			if json.Unmarshal(data, &value) == nil && value != nil {
				raw = data
				break
			}
		}
	}
	var value map[string]any
	json.Unmarshal(raw, &value)
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []map[string]any{}
	for _, key := range keys {
		if !whitelistIP.MatchString(key) {
			continue
		}
		if label, ok := value[key].(string); ok {
			result = append(result, map[string]any{"ip": key, "label": label})
		}
	}
	return result
}
