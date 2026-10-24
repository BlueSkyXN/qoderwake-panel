package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"

	"qoderwake-panel/internal/apierror"
)

const Hidden = "__PANEL_KEEP_SECRET__"

var credentialField = regexp.MustCompile(`(?i)(api.?key|secret|token|password|passwd|authorization|cookie|headers|credentials?|private.?key)$`)

// RedactedConfig leaves extension fields editable while keeping credentials on the server.
func RedactedConfig(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			if credentialField.MatchString(k) {
				out[k] = Hidden
			} else {
				out[k] = RedactedConfig(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = RedactedConfig(item)
		}
		return out
	default:
		return value
	}
}

// DecodeConfig rejects duplicate JSON keys instead of silently dropping an earlier value.
func DecodeConfig(raw string) (map[string]any, error) {
	if len(raw) > 60000 {
		return nil, apierror.Invalid("provider_settings_too_large")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var parse func(int) (any, error)
	parse = func(depth int) (any, error) {
		if depth > 40 {
			return nil, apierror.Invalid("provider_settings_invalid")
		}
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t {
		case json.Delim('{'):
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, apierror.Invalid("provider_settings_invalid")
				}
				if _, exists := m[k]; exists {
					return nil, apierror.Invalid("provider_duplicate_field")
				}
				v, err := parse(depth + 1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			_, err := d.Token()
			return m, err
		case json.Delim('['):
			a := []any{}
			for d.More() {
				v, err := parse(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := d.Token()
			return a, err
		default:
			return t, nil
		}
	}
	v, err := parse(0)
	if err != nil {
		return nil, apierror.Invalid("provider_settings_invalid")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, apierror.Invalid("provider_settings_invalid")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, apierror.Invalid("provider_settings_invalid")
	}
	return m, nil
}

func (s *Settings) Editor(name string) (map[string]any, error) {
	snapshot, err := s.Read()
	if err != nil {
		return nil, err
	}
	value, exists := snapshot.Data["providers"].(map[string]any)[name]
	if !exists {
		return nil, apierror.New(404, "provider_not_found")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, apierror.Invalid("provider_schema_unsupported")
	}
	return map[string]any{"name": name, "configJSON": ConfigJSON(RedactedConfig(value)), "baseRevision": snapshot.Revision, "storage": "legacy_plaintext"}, nil
}

func hasSecret(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if credentialField.MatchString(k) || hasSecret(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasSecret(v) {
				return true
			}
		}
	}
	return false
}

func restoreCredentials(draft, old any) (any, error) {
	switch d := draft.(type) {
	case map[string]any:
		o, _ := old.(map[string]any)
		for k, v := range d {
			if credentialField.MatchString(k) {
				if v != Hidden {
					return nil, apierror.Invalid("provider_secret_edit_required")
				}
				previous, exists := o[k]
				if !exists {
					return nil, apierror.Invalid("provider_secret_edit_required")
				}
				d[k] = previous
			} else {
				x, err := restoreCredentials(v, o[k])
				if err != nil {
					return nil, err
				}
				d[k] = x
			}
		}
		for k, v := range o {
			if _, exists := d[k]; !exists && (credentialField.MatchString(k) || hasSecret(v)) {
				d[k] = v
			}
		}
		return d, nil
	case []any:
		o, _ := old.([]any)
		for i, v := range d {
			var previous any
			if i < len(o) {
				previous = o[i]
			}
			if row, ok := v.(map[string]any); ok && row["model"] != nil {
				previous = nil
				for _, item := range o {
					if m, ok := item.(map[string]any); ok && m["model"] == row["model"] {
						previous = item
						break
					}
				}
			}
			x, err := restoreCredentials(v, previous)
			if err != nil {
				return nil, err
			}
			d[i] = x
		}
		return d, nil
	default:
		if hasSecret(old) {
			return nil, apierror.Invalid("provider_secret_edit_required")
		}
		return draft, nil
	}
}

func ValidateConfig(draft map[string]any, old map[string]any) error {
	address, ok := draft["baseUrl"].(string)
	if !ok {
		return apierror.Invalid("invalid_provider_url")
	}
	// Existing HTTP endpoints remain editable; changing an endpoint still uses the normal policy.
	if address == old["baseUrl"] {
		u, err := url.Parse(address)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return apierror.Invalid("invalid_provider_url")
		}
	} else if _, err := ValidateURL(address); err != nil {
		return err
	}
	models, ok := draft["models"].([]any)
	if !ok || len(models) == 0 || len(models) > 200 {
		return apierror.Invalid("invalid_provider_models")
	}
	seen := map[string]bool{}
	for _, raw := range models {
		row, ok := raw.(map[string]any)
		if !ok {
			return apierror.Invalid("invalid_provider_models")
		}
		model, ok := row["model"].(string)
		if !ok || strings.TrimSpace(model) == "" || model != strings.TrimSpace(model) || len(model) > 300 || strings.ContainsAny(model, "\r\n\x00") || seen[model] {
			return apierror.Invalid("invalid_provider_models")
		}
		seen[model] = true
		for _, field := range []string{"contextWindow", "maxOutputTokens"} {
			if v, exists := row[field]; exists {
				n, ok := v.(json.Number)
				if !ok {
					return apierror.Invalid("invalid_provider_models")
				}
				value, e := n.Int64()
				if e != nil || value <= 0 {
					return apierror.Invalid("invalid_provider_models")
				}
			}
		}
	}
	return nil
}

func (s *Settings) WriteConfig(name, raw, keyAction, key, expected string) (Snapshot, error) {
	if !NamePattern.MatchString(name) || len(name) > 120 {
		return Snapshot{}, apierror.Invalid("invalid_provider_name")
	}
	draft, err := DecodeConfig(raw)
	if err != nil {
		return Snapshot{}, err
	}
	if keyAction != "keep" && keyAction != "replace" && keyAction != "delete" {
		return Snapshot{}, apierror.Invalid("invalid_key_action")
	}
	if keyAction == "replace" && (strings.TrimSpace(key) == "" || len(key) > 8192 || strings.ContainsAny(key, "\r\n\x00") || key == Hidden) {
		return Snapshot{}, apierror.Invalid("invalid_provider_key")
	}
	if keyAction != "replace" && key != "" {
		return Snapshot{}, apierror.Invalid("invalid_key_action")
	}
	return s.Update(expected, func(data map[string]any) error {
		providers := data["providers"].(map[string]any)
		old, exists := providers[name].(map[string]any)
		if !exists {
			return apierror.New(404, "provider_not_found")
		}
		if err := ValidateConfig(draft, old); err != nil {
			return err
		}
		// apiKey uses an explicit separate action, including deletion.
		if v, exists := draft["apiKey"]; exists && v != Hidden {
			return apierror.Invalid("provider_secret_edit_required")
		}
		value, err := restoreCredentials(draft, old)
		if err != nil {
			return err
		}
		item := value.(map[string]any)
		switch keyAction {
		case "replace":
			item["apiKey"] = key
		case "delete":
			delete(item, "apiKey")
		}
		providers[name] = item
		return nil
	})
}

// ConfigJSON is also used by tests to keep json.Number values intact.
func ConfigJSON(value any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetIndent("", "  ")
	e.SetEscapeHTML(false)
	_ = e.Encode(value)
	return b.String()
}
