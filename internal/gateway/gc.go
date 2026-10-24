package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type generationManifest struct {
	Schema     int        `json:"schemaVersion"`
	Generation Generation `json:"generation"`
}

func (m *Manager) gc(ctx context.Context) (map[string]any, error) {
	entries, err := os.ReadDir(m.store())
	if err != nil {
		return nil, err
	}
	removed, retained := 0, 0
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !entry.IsDir() || !generationRE.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(m.store(), entry.Name())
		raw, err := regular(filepath.Join(path, "manifest.json"), 65536)
		var manifest generationManifest
		if err != nil || decodeStrict(raw, &manifest) != nil || manifest.Schema != ProtocolVersion || manifest.Generation.Path != path || m.generationValid(manifest.Generation) != nil {
			retained++
			continue
		}
		if err = m.removeGeneration(manifest.Generation); err != nil {
			retained++
			continue
		}
		removed++
	}
	return map[string]any{"ok": true, "removed": removed, "retained": retained}, nil
}
func (m *Manager) GC(ctx context.Context) (map[string]any, error) {
	release, err := m.locked()
	if err != nil {
		return nil, err
	}
	defer release()
	if journal, err := m.readJournal(); err != nil {
		return nil, err
	} else if journal != nil {
		return nil, errors.New("gateway_recovery_required")
	}
	return m.gc(ctx)
}
