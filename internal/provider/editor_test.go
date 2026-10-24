package provider

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
)

const multiFixture = `{"other":{"large":9007199254740995},"providers":{"multi":{"baseUrl":"http://models.example.test/v1","protocol":"openai-responses","type":"openai-compatible","authType":"bearer","apiKey":"synthetic-secret-value","headers":{"X-Custom":"synthetic-header-value"},"extra":{"large":9007199254740995},"models":[{"model":"one","contextWindow":500000,"maxOutputTokens":64000,"capabilities":{"thinking":{"enabled":true,"supportedEffortLevels":["high","max"]}}},{"model":"two","unknown":{"keep":true}}]}}}`

func multiSettings(t *testing.T) *Settings {
	t.Helper()
	s := setupSettings(t)
	if err := os.WriteFile(s.Path, []byte(multiFixture), 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMultiModelEditorKeepsSecretsProtocolExtensionsAndNumbers(t *testing.T) {
	s := multiSettings(t)
	draft, err := s.Editor("multi")
	if err != nil {
		t.Fatal(err)
	}
	raw := draft["configJSON"].(string)
	if strings.Contains(raw, "synthetic-") || !strings.Contains(raw, `"maxOutputTokens": 64000`) || !strings.Contains(raw, "9007199254740995") {
		t.Fatal("redaction or number fidelity failure")
	}
	raw = strings.Replace(raw, `"maxOutputTokens": 64000`, `"maxOutputTokens": 32000`, 1)
	snapshot, err := s.WriteConfig("multi", raw, "keep", "", draft["baseRevision"].(string))
	if err != nil {
		t.Fatal(err)
	}
	value := snapshot.Data["providers"].(map[string]any)["multi"].(map[string]any)
	if value["apiKey"] != "synthetic-secret-value" || value["protocol"] != "openai-responses" || value["baseUrl"] != "http://models.example.test/v1" {
		t.Fatal("connection fields changed")
	}
	if value["headers"].(map[string]any)["X-Custom"] != "synthetic-header-value" {
		t.Fatal("header lost")
	}
	models := value["models"].([]any)
	if len(models) != 2 || models[0].(map[string]any)["maxOutputTokens"] != json.Number("32000") || models[1].(map[string]any)["unknown"].(map[string]any)["keep"] != true {
		t.Fatal("model metadata lost")
	}
	backup, err := os.ReadFile(s.Recovery)
	if err != nil || string(backup) != multiFixture {
		t.Fatal("recovery mismatch")
	}
	for _, path := range []string{s.Path, s.Recovery} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("credentials permissions")
		}
	}
}

func TestExplicitCredentialActionsAndConflicts(t *testing.T) {
	s := multiSettings(t)
	draft, _ := s.Editor("multi")
	raw, rev := draft["configJSON"].(string), draft["baseRevision"].(string)
	if _, err := s.WriteConfig("multi", raw, "replace", "", rev); err == nil {
		t.Fatal("blank replacement accepted")
	}
	if _, err := s.WriteConfig("multi", raw, "keep", "accidental-input", rev); err == nil {
		t.Fatal("ambiguous replacement accepted")
	}
	if _, err := s.WriteConfig("multi", raw, "keep|replace", "", rev); err == nil {
		t.Fatal("invalid credential action accepted")
	}
	after, err := s.WriteConfig("multi", raw, "replace", "synthetic-replacement", rev)
	if err != nil {
		t.Fatal(err)
	}
	if after.Data["providers"].(map[string]any)["multi"].(map[string]any)["apiKey"] != "synthetic-replacement" {
		t.Fatal("key not replaced")
	}
	if _, err := s.WriteConfig("multi", raw, "delete", "", rev); err == nil {
		t.Fatal("stale delete accepted")
	}
	after, err = s.WriteConfig("multi", raw, "delete", "", after.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := after.Data["providers"].(map[string]any)["multi"].(map[string]any)["apiKey"]; exists {
		t.Fatal("key not deleted")
	}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	draft, _ = s.Editor("multi")
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.WriteConfig("multi", draft["configJSON"].(string), "replace", "synthetic-concurrent", after.Revision)
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	passed := 0
	for err := range errors {
		if err == nil {
			passed++
		}
	}
	if passed != 1 {
		t.Fatal("concurrent writers both committed", passed)
	}
}

func TestEditorRejectsInvalidJSONAndChangedHTTPDestination(t *testing.T) {
	for _, raw := range []string{`{"models":[],"models":[{"model":"one"}]}`, `[]`, `{"x":1} {}`, `{"a":}`} {
		if _, err := DecodeConfig(raw); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	s := multiSettings(t)
	draft, _ := s.Editor("multi")
	raw, rev := draft["configJSON"].(string), draft["baseRevision"].(string)
	for _, bad := range []string{
		strings.Replace(raw, "models.example.test", "another.example.test", 1),
		strings.Replace(raw, `"model": "two"`, `"model": "one"`, 1),
		strings.Replace(raw, `"contextWindow": 500000`, `"contextWindow": 0`, 1),
		strings.Replace(raw, `"apiKey": "`+Hidden+`"`, `"apiKey": "inline-secret"`, 1),
	} {
		if _, err := s.WriteConfig("multi", bad, "keep", "", rev); err == nil {
			t.Fatal("invalid edit accepted")
		}
		current, _ := s.Read()
		if current.Revision != rev {
			t.Fatal("failed edit changed settings")
		}
	}
}

func TestModelSecretMovesWithModelWhenReordering(t *testing.T) {
	draft := []any{map[string]any{"model": "two", "headers": Hidden}, map[string]any{"model": "one", "headers": Hidden}}
	old := []any{map[string]any{"model": "one", "headers": "synthetic-one"}, map[string]any{"model": "two", "headers": "synthetic-two"}}
	value, err := restoreCredentials(draft, old)
	if err != nil || value.([]any)[0].(map[string]any)["headers"] != "synthetic-two" {
		t.Fatal("model credentials crossed")
	}
}
