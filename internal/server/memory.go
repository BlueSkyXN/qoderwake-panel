package server

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/daemon"
)

const memoryTextLimit = 200 * 1024
const memoryItemLimit = 2000

var memoryPath = regexp.MustCompile(`^[A-Za-z0-9_./ -]{1,200}$`)

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func memoryDoc(value any) (map[string]any, error) {
	row, ok := value.(map[string]any)
	if !ok {
		return nil, apierror.Invalid("invalid_memory_doc")
	}
	path, ok := row["path"].(string)
	if !ok || !memoryPath.MatchString(path) || strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.HasPrefix(path, ".") {
		return nil, apierror.Invalid("invalid_memory_doc")
	}
	content := any("")
	if value, exists := row["content"]; exists {
		content = value
	}
	text, ok := content.(string)
	if !ok {
		return nil, apierror.Invalid("invalid_memory_doc")
	}
	result := map[string]any{"path": path, "content": truncateUTF8(text, memoryTextLimit)}
	if len(text) > memoryTextLimit {
		result["truncated"] = true
	}
	return result, nil
}
func (s *Server) memoryFiles(ctx context.Context, wid string) (map[string]any, error) {
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid+"/memory?view=all", nil)
	if err != nil {
		return nil, err
	}
	data, ok := raw["data"].(map[string]any)
	scope := str(data, "scopeId")
	if scope == "" {
		scope = str(data, "agentId")
	}
	if !ok || scope != wid {
		return nil, apierror.Invalid("memory_unavailable")
	}
	result := map[string]any{"scope": data["scope"], "truncated": false}
	docs := []map[string]any{}
	if data["index"] != nil {
		doc, e := memoryDoc(data["index"])
		if e != nil {
			return nil, e
		}
		docs = append(docs, doc)
	}
	for _, section := range []string{"topics", "sessions"} {
		if data[section] == nil {
			continue
		}
		values, ok := data[section].([]any)
		if !ok {
			return nil, apierror.Invalid("invalid_memory_doc")
		}
		count := min(len(values), max(0, memoryItemLimit-len(docs)))
		if len(values) > count {
			result["truncated"] = true
		}
		for _, value := range values[:count] {
			doc, e := memoryDoc(value)
			if e != nil {
				return nil, e
			}
			docs = append(docs, doc)
		}
	}
	for _, doc := range docs {
		if doc["truncated"] == true {
			result["truncated"] = true
		}
	}
	result["docs"] = docs
	return result, nil
}
func avatarMemory(value any) (map[string]any, error) {
	row, ok := value.(map[string]any)
	if !ok {
		return nil, apierror.Invalid("invalid_memory_record")
	}
	result := map[string]any{}
	for key, limit := range map[string]int{"memoryId": 200, "title": 1000, "content": memoryTextLimit, "updatedAt": 100, "securityLevel": 100} {
		value, exists := row[key]
		if !exists {
			value = ""
		}
		text, ok := value.(string)
		if !ok {
			return nil, apierror.Invalid("invalid_memory_record")
		}
		if len(text) > limit {
			if key != "content" {
				return nil, apierror.Invalid("invalid_memory_record")
			}
			text = truncateUTF8(text, limit)
			result["truncated"] = true
		}
		result[key] = text
	}
	if result["memoryId"] == "" || result["content"] == "" {
		return nil, apierror.Invalid("invalid_memory_record")
	}
	return result, nil
}
func memoryFailure(err error) string {
	var conflict *daemon.Conflict
	if errors.As(err, &conflict) {
		return conflict.Code
	}
	var de *daemon.Error
	var ae *apierror.Error
	if errors.As(err, &de) {
		return de.Code
	}
	if errors.As(err, &ae) && ae.Code == "version_conflict" {
		return ae.Code
	}
	return "memory_unavailable"
}
func (s *Server) memoryData(ctx context.Context, wid string) (any, error) {
	if _, err := safeID(wid); err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid, nil)
	if err != nil {
		return nil, err
	}
	agent, ok := raw["data"].(map[string]any)
	id := str(agent, "agentId")
	if id == "" {
		id = str(agent, "id")
	}
	if !ok || id != wid {
		return nil, apierror.Invalid("invalid_waker_detail")
	}
	if agent["employeeType"] != "personal_avatar" {
		files, e := s.memoryFiles(ctx, wid)
		if e != nil {
			return nil, e
		}
		files["kind"] = "files"
		files["status"] = "ready"
		return files, nil
	}
	result := map[string]any{"kind": "personal_avatar", "status": "ready", "memories": []map[string]any{}, "docs": []map[string]any{}, "filesStatus": "ready", "truncated": false}
	readGraph := func() error {
		raw, e := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid+"/personal-avatar/learning/graph", nil)
		if e != nil {
			return e
		}
		graph, ok := raw["data"].(map[string]any)
		if !ok {
			return apierror.Invalid("invalid_memory_record")
		}
		values, ok := graph["memories"].([]any)
		if !ok {
			return apierror.Invalid("invalid_memory_record")
		}
		memories := []map[string]any{}
		truncated := len(values) > memoryItemLimit
		for _, value := range values[:min(len(values), memoryItemLimit)] {
			record, e := avatarMemory(value)
			if e != nil {
				return e
			}
			memories = append(memories, record)
			truncated = truncated || record["truncated"] == true
		}
		result["memories"] = memories
		result["truncated"] = truncated
		return nil
	}
	if err = readGraph(); err != nil {
		result["status"] = "unavailable"
		result["error"] = memoryFailure(err)
	}
	if files, e := s.memoryFiles(ctx, wid); e != nil {
		result["filesStatus"] = "unavailable"
		result["filesError"] = memoryFailure(e)
	} else {
		result["docs"] = files["docs"]
		result["truncated"] = result["truncated"] == true || files["truncated"] == true
	}
	return result, nil
}
