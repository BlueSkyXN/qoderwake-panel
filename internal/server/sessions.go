package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/daemon"
)

func textValue(value any, limit int, empty bool) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", apierror.Invalid("invalid_text")
	}
	s = strings.TrimSpace(s)
	if (!empty && s == "") || len([]rune(s)) > limit {
		return "", apierror.Invalid("invalid_text")
	}
	return s, nil
}
func queryInt(b map[string]any, key string, fallback, min, max int) (int, error) {
	raw := str(b, key)
	if raw == "" {
		return fallback, nil
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < min || n > max {
		return 0, apierror.Invalid("invalid_pagination")
	}
	return n, nil
}
func uuid() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
func (s *Server) consoleSessions(ctx context.Context, b map[string]any) (any, error) {
	wid, err := safeID(str(b, "waker"))
	if err != nil {
		return nil, err
	}
	offset, err := queryInt(b, "offset", 0, 0, 100000)
	if err != nil {
		return nil, err
	}
	limit, err := queryInt(b, "limit", 30, 1, 100)
	if err != nil {
		return nil, err
	}
	q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}, "pinned_first": {"true"}, "task_source": {"all"}}
	if origin := str(b, "origin"); origin != "" {
		if _, err = safeID(origin); err != nil {
			return nil, apierror.Invalid("invalid_session_origin")
		}
		q.Set("origins", origin)
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/agents/"+wid+"/console-sessions/query?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	data := daemon.Data(raw)
	out := []map[string]any{}
	for _, row := range rows(data["items"]) {
		if value := row["local_agent_id"]; value != nil && value != "" && value != wid {
			continue
		}
		out = append(out, pick(row, "session_id", "title", "origin", "session_status", "unread", "connection_status", "created_at", "updated_at"))
	}
	return map[string]any{"items": out, "has_more": data["has_more"] == true}, nil
}
func (s *Server) sessionArtifacts(ctx context.Context, sid string) (map[string]any, error) {
	if _, err := opaqueID(sid); err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/sessions/"+url.PathEscape(sid)+"/artifacts", nil)
	if err != nil {
		return nil, err
	}
	data := daemon.Data(raw)
	out := []map[string]any{}
	for _, row := range rows(data["artifacts"]) {
		out = append(out, pick(row, "id", "type", "title", "description", "relativePath", "mimeType", "filesChanged"))
	}
	return map[string]any{"target": data["target"], "artifacts": out}, nil
}
func (s *Server) artifactFile(ctx context.Context, sid, artifact string) (any, error) {
	if _, err := opaqueID(artifact); err != nil {
		return nil, err
	}
	data, err := s.sessionArtifacts(ctx, sid)
	if err != nil {
		return nil, err
	}
	found := false
	for _, row := range data["artifacts"].([]map[string]any) {
		found = found || row["id"] == artifact
	}
	if !found {
		return nil, apierror.New(404, "artifact_not_found")
	}
	transport, ok := s.Daemon.(interface {
		Raw(context.Context, string, string, any, int64) ([]byte, int, http.Header, error)
	})
	if !ok {
		return nil, apierror.New(502, "daemon_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, status, headers, err := transport.Raw(ctx, "GET", "/api/sessions/"+url.PathEscape(sid)+"/artifacts/"+url.PathEscape(artifact)+"/file", nil, 32*1024*1024)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || status == 202 {
		return nil, daemon.RequestError(status, nil)
	}
	contentType, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil || contains([]string{"text/html", "application/xhtml+xml", "image/svg+xml"}, contentType) {
		contentType = "application/octet-stream"
	}
	return download{body, "artifact.bin", contentType}, nil
}
func (s *Server) chatSessions(ctx context.Context, wid string) (any, error) {
	if _, err := safeID(wid); err != nil {
		return nil, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/sessions?limit=30&agentId="+wid, nil)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, row := range rows(raw["data"]) {
		if agent := str(row, "agentId"); agent != "" && agent != wid {
			continue
		}
		name := str(row, "title")
		if name == "" {
			name = "(未命名)"
		}
		at := []rune(str(row, "startedAt"))
		at = at[:min(16, len(at))]
		out = append(out, map[string]any{"id": row["sessionId"], "title": name, "status": row["status"], "startedAt": strings.ReplaceAll(string(at), "T", " ")})
	}
	sort.SliceStable(out, func(i, j int) bool { return str(out[i], "startedAt") > str(out[j], "startedAt") })
	return out[:min(len(out), 100)], nil
}
func (s *Server) chatNew(ctx context.Context, wid, title string) (string, error) {
	if _, err := safeID(wid); err != nil {
		return "", err
	}
	if title == "" {
		title = "面板会话"
	}
	raw, err := s.Daemon.JSON(ctx, "POST", "/api/agents/"+wid+"/sessions", map[string]any{"title": title, "user_message_id": uuid(), "events": []any{}})
	if err != nil {
		return "", err
	}
	return str(daemon.Data(raw), "sessionId"), nil
}
func (s *Server) chatSend(ctx context.Context, sid, message string) (bool, error) {
	if _, err := safeID(sid); err != nil {
		return false, err
	}
	payload := map[string]any{"user_message_id": uuid(), "events": []any{map[string]any{"event_type": "user", "payload": map[string]any{"type": "user", "uuid": uuid(), "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": message}}}}}}}
	raw, err := s.Daemon.JSON(ctx, "POST", "/api/sessions/"+sid+"/events", payload)
	return raw["success"] == true, err
}

type chatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type chatResult struct {
	Messages []chatMessage `json:"messages"`
	Status   string        `json:"status"`
}

func foldEvents(events any) chatResult {
	result := chatResult{Messages: []chatMessage{}, Status: "idle"}
	index := map[string]int{}
	for _, event := range rows(events) {
		p, _ := event["payload"].(map[string]any)
		kind := str(p, "type")
		if kind == "user" {
			result.Status = "open"
		}
		if kind == "result" {
			result.Status = str(p, "subtype")
			if result.Status == "" {
				result.Status = "done"
			}
			continue
		}
		if kind != "user" && kind != "assistant" {
			continue
		}
		message, _ := p["message"].(map[string]any)
		content, ok := message["content"].(string)
		if !ok {
			parts := []string{}
			for _, part := range rows(message["content"]) {
				if part["type"] == "text" {
					parts = append(parts, str(part, "text"))
				}
			}
			content = strings.Join(parts, "\n")
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		mid := str(message, "id")
		if mid == "" {
			mid = str(p, "uuid")
		}
		if mid == "" {
			mid = str(event, "event_id")
		}
		if mid == "" {
			mid = str(event, "id")
		}
		entry := chatMessage{kind, content}
		if mid != "" {
			key := kind + "\x00" + mid
			if i, exists := index[key]; exists {
				result.Messages[i] = entry
				continue
			}
			index[key] = len(result.Messages)
		}
		result.Messages = append(result.Messages, entry)
	}
	return result
}
func (s *Server) chatMessages(ctx context.Context, sid string) (chatResult, error) {
	if _, err := safeID(sid); err != nil {
		return chatResult{}, err
	}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/sessions/"+sid+"/events", nil)
	if err != nil {
		return chatResult{}, err
	}
	data := raw["data"]
	if object, ok := data.(map[string]any); ok {
		data = object["data"]
		if data == nil {
			data = object["events"]
		}
	}
	return foldEvents(data), nil
}
func (s *Server) gatewayAsk(ctx context.Context, wid, message string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	sid, err := s.chatNew(ctx, wid, "API 会话")
	if err != nil {
		return nil, err
	}
	ok, err := s.chatSend(ctx, sid, message)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]any{"ok": false, "message": "发送失败"}, nil
	}
	for {
		result, err := s.chatMessages(ctx, sid)
		if err != nil {
			return nil, err
		}
		if result.Status != "open" && result.Status != "idle" {
			replies := []string{}
			for _, msg := range result.Messages {
				if msg.Role == "assistant" {
					replies = append(replies, msg.Text)
				}
			}
			message := strings.Join(replies, "\n")
			if message == "" {
				message = "回合结束，无文本回复"
			}
			return map[string]any{"ok": result.Status == "success" && len(replies) > 0, "message": message}, nil
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return map[string]any{"ok": false, "message": "等待超时；请求已提交，请在聊天记录查看，勿重复发送"}, nil
		case <-timer.C:
		}
	}
}
func (s *Server) sessionPost(ctx context.Context, path string, b map[string]any) (any, error) {
	switch path {
	case "/api/chat/new":
		title := any("新会话")
		if value, exists := b["title"]; exists {
			title = value
		}
		name, err := textValue(title, 100, false)
		if err != nil {
			return nil, err
		}
		sid, err := s.chatNew(ctx, str(b, "wakerId"), name)
		return map[string]any{"ok": sid != "", "id": sid}, err
	case "/api/chat/send", "/api/gw":
		message, err := textValue(b["message"], 16000, false)
		if err != nil {
			return nil, err
		}
		if path == "/api/gw" {
			return s.gatewayAsk(ctx, str(b, "wakerId"), message)
		}
		ok, err := s.chatSend(ctx, str(b, "sessionId"), message)
		return map[string]any{"ok": ok}, err
	}
	sid, err := opaqueID(str(b, "sessionId"))
	if err != nil {
		return nil, err
	}
	endpoint := "/api/sessions/" + url.PathEscape(sid)
	if path == "/api/session/read" {
		payload := map[string]any{}
		if b["lastSeq"] != nil {
			n, e := integer(b, "lastSeq")
			if e != nil || n < 0 {
				return nil, apierror.Invalid("invalid_sequence")
			}
			payload["last_seq"] = n
		}
		_, err = s.Daemon.JSON(ctx, "POST", endpoint+"/read", payload)
	} else {
		if str(b, "confirm") != path {
			return nil, apierror.New(409, "confirmation_required")
		}
		force := false
		if value, exists := b["force"]; exists {
			var ok bool
			force, ok = value.(bool)
			if !ok {
				return nil, apierror.Invalid("invalid_force")
			}
		}
		if force {
			endpoint += "?force=true"
		}
		_, err = s.Daemon.JSON(ctx, "DELETE", endpoint, nil)
	}
	return map[string]any{"ok": true}, err
}
