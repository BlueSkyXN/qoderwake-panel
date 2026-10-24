package server

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/daemon"
)

func (s *Server) automations(ctx context.Context, b map[string]any, runs bool) (any, error) {
	page, err := queryInt(b, "page", 1, 1, 100000)
	if err != nil {
		return nil, err
	}
	path := "/api/triggers"
	keys := []string{"id", "triggerId", "triggerName", "taskDescription", "model", "enabled", "maxRuns", "endDate", "location", "executionTarget"}
	if runs {
		id, e := safeID(str(b, "id"))
		if e != nil {
			return nil, e
		}
		path += "/" + id + "/runs"
		keys = []string{"runId", "triggerId", "startedAt", "finishedAt", "status", "sessionId", "runSequence", "location"}
	}
	raw, err := s.Daemon.JSON(ctx, "GET", path+"?page="+strconv.Itoa(page)+"&pageSize=20", nil)
	if err != nil {
		return nil, err
	}
	data := daemon.Data(raw)
	items := []map[string]any{}
	for _, row := range rows(data["items"]) {
		items = append(items, pick(row, keys...))
	}
	pagination := data["pagination"]
	if pagination == nil {
		pagination = map[string]any{}
	}
	return map[string]any{"items": items, "pagination": pagination}, nil
}
func (s *Server) pendingPairings(ctx context.Context, limit, offset int) (map[string]any, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, apierror.Invalid("invalid_pagination")
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}, "includeTotal": {"true"}}
	raw, err := s.Daemon.JSON(ctx, "GET", "/api/channels/pairing/pending?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	data := daemon.Data(raw)
	items := []map[string]any{}
	for _, row := range rows(data["items"]) {
		items = append(items, pick(row, "pendingId", "pendingRevision", "remoteExpiresAt", "channelId", "robotId", "bindingKey", "conversationType", "senderId", "senderStaffId", "conversationId", "conversationName", "subjectName", "displayName", "createdAt", "updatedAt", "corpId", "corpName"))
	}
	total := data["total"]
	if total == nil {
		total = 0
	}
	return map[string]any{"items": items, "total": total}, nil
}

var integerRevision = regexp.MustCompile(`^-?[0-9]+$`)

func pairingRevision(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return "s:" + v, nil
	case json.Number:
		if integerRevision.MatchString(string(v)) {
			return "n:" + string(v), nil
		}
	case int:
		return "n:" + strconv.Itoa(v), nil
	case int64:
		return "n:" + strconv.FormatInt(v, 10), nil
	}
	return "", apierror.Invalid("pairing_revision_required")
}
func (s *Server) pairingApproval(ctx context.Context, b map[string]any) (map[string]any, error) {
	id, err := opaqueID(str(b, "pendingId"))
	if err != nil {
		return nil, err
	}
	revision, err := pairingRevision(b["pendingRevision"])
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if _, ok := b["pendingOffset"]; ok {
		offset, err = integer(b, "pendingOffset")
		if err != nil {
			return nil, err
		}
	}
	if offset < 0 || offset > 100000 {
		return nil, apierror.Invalid("invalid_pagination")
	}
	page, err := s.pendingPairings(ctx, 100, int(offset))
	if err != nil {
		return nil, err
	}
	var current map[string]any
	for _, row := range page["items"].([]map[string]any) {
		if row["pendingId"] == id {
			current = row
			break
		}
	}
	value, err := pairingRevision(current["pendingRevision"])
	if current == nil || err != nil || revision != value {
		return nil, apierror.New(409, "version_conflict")
	}
	required := []string{"channelId", "senderId", "conversationId", "bindingKey", "conversationType"}
	for _, key := range required {
		if !reflect.DeepEqual(b[key], current[key]) {
			return nil, apierror.New(409, "version_conflict")
		}
	}
	expires, err := time.Parse(time.RFC3339Nano, str(current, "remoteExpiresAt"))
	if err != nil || !expires.After(time.Now()) {
		return nil, apierror.New(409, "pairing_request_expired_or_unverified")
	}
	wid, err := safeID(str(b, "wakerId"))
	if err != nil {
		return nil, err
	}
	if _, err = s.waker(ctx, wid); err != nil {
		return nil, err
	}
	payload := map[string]any{}
	for _, key := range required {
		value, err := textValue(current[key], 500, false)
		if err != nil {
			return nil, err
		}
		payload[key] = value
	}
	for _, pair := range [][2]string{{"senderStaffId", "senderStaffId"}, {"conversationName", "conversationName"}, {"senderName", "displayName"}} {
		value := current[pair[1]]
		if pair[0] == "senderName" && (value == nil || value == "") {
			value = current["subjectName"]
		}
		if value == nil {
			value = ""
		}
		limit := 500
		if pair[0] == "senderName" {
			limit = 200
		}
		payload[pair[0]], err = textValue(value, limit, true)
		if err != nil {
			return nil, err
		}
	}
	payload["targets"] = []any{map[string]any{"targetKind": "waker", "targetId": wid, "enabled": true}}
	payload["model"] = "auto"
	payload["workspace"] = nil
	payload["intermediateReplyMode"] = "off"
	payload["allowQoderwakeCommands"] = false
	payload["allowCustomModel"] = false
	return payload, nil
}
func (s *Server) automationPost(ctx context.Context, identity auth.Identity, path string, b map[string]any) (any, error) {
	if str(b, "confirm") != path {
		return nil, apierror.New(409, "confirmation_required")
	}
	if path != "/api/automation/delete" {
		release, err := s.writeLock(identity)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if path == "/api/pairing/approve" {
		payload, err := s.pairingApproval(ctx, b)
		if err != nil {
			return nil, err
		}
		_, err = s.Daemon.JSON(ctx, "POST", "/api/channels/pairing/approve", payload)
		return map[string]any{"ok": true}, err
	}
	id, err := safeID(str(b, "id"))
	if err != nil {
		return nil, err
	}
	method, endpoint, payload := "DELETE", "/api/triggers/"+id, map[string]any{}
	switch path {
	case "/api/automation/toggle":
		enabled, ok := b["enabled"].(bool)
		if !ok {
			return nil, apierror.Invalid("invalid_enabled")
		}
		method = "PATCH"
		payload["enabled"] = enabled
	case "/api/automation/run":
		method = "POST"
		endpoint += "/run-now"
		if b["subTriggerId"] != nil && b["subTriggerId"] != "" {
			sub, e := safeID(str(b, "subTriggerId"))
			if e != nil {
				return nil, e
			}
			payload["subTriggerId"] = sub
		}
	}
	_, err = s.Daemon.JSON(ctx, method, endpoint, payload)
	return map[string]any{"ok": true}, err
}
