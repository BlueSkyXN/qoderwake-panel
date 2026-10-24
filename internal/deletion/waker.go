package deletion

import (
	"encoding/json"
	"math"
	"time"
)

func numeric(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		v, e := n.Float64()
		return v, e == nil
	}
	return 0, false
}
func counter(value any) (int64, bool) {
	if n, ok := value.(json.Number); ok {
		v, e := n.Int64()
		return v, e == nil && v >= 0
	}
	n, ok := numeric(value)
	return int64(n), ok && n >= 0 && math.Trunc(n) == n && n < math.MaxInt64
}
func (s Scanner) loadList(name, target string) ([]any, bool) {
	value, e := s.Source(name, target)
	if e != nil {
		return nil, false
	}
	return list(value)
}
func (s Scanner) waker(target string) ([]Check, string) {
	checks := []Check{}
	detail, e := s.Source("waker_detail", target)
	row, valid := object(detail)
	id := text(row["agentId"])
	if id == "" {
		id = text(row["id"])
	}
	if e != nil || !valid || id != target {
		checks = append(checks, unknown("system_identity", "系统 Waker 保护", true))
	} else {
		protected := row["systemIdentity"] != nil
		c := check("system_identity", "系统 Waker 保护", "clear", true, 0, nil, "")
		if protected {
			c.Status = "blocked"
			c.Count = 1
			c.Refs = []string{"systemIdentity"}
		}
		checks = append(checks, c)
	}
	if callers, ok := s.loadList("callers", ""); !ok {
		checks = append(checks, unknown("caller_scope", "Panel 调用者授权", true))
	} else {
		matched := []any{}
		for _, value := range callers {
			r, ok := object(value)
			if !ok {
				continue
			}
			scopes, _ := list(r["wakers"])
			contains := false
			for _, w := range scopes {
				contains = contains || w == target
			}
			expires, ok := numeric(r["expires"])
			revoked, _ := numeric(r["revoked"])
			if contains && r["revoked"] != true && revoked != 1 && ok && expires > float64(time.Now().Unix()) {
				matched = append(matched, r)
			}
		}
		checks = append(checks, matching("caller_scope", "Panel 调用者授权", "blocked", true, matched, "id", "name"))
	}
	if rows, ok := s.loadList("triggers", ""); !ok {
		checks = append(checks, unknown("trigger_target", "Trigger 目标", true))
	} else {
		matched := []any{}
		valid := true
		for _, r := range rows {
			values, e := targets(r)
			if e != nil || len(values) == 0 {
				valid = false
				break
			}
			if values[target] {
				matched = append(matched, r)
			}
		}
		if !valid {
			checks = append(checks, unknown("trigger_target", "Trigger 目标", true))
		} else {
			checks = append(checks, matching("trigger_target", "Trigger 目标", "blocked", true, matched, "triggerId", "id", "triggerName"))
		}
	}
	if rows, ok := s.loadList("channels", ""); !ok {
		checks = append(checks, unknown("channel_target", "Channel 绑定目标", true))
	} else {
		matched := []any{}
		valid := true
		for _, r := range rows {
			values, e := channelTargets(r)
			if e != nil {
				valid = false
				continue
			}
			if values[target] {
				matched = append(matched, r)
			}
		}
		if !valid {
			checks = append(checks, unknown("channel_target", "Channel 绑定目标", true))
		} else {
			checks = append(checks, matching("channel_target", "Channel 绑定目标", "blocked", true, matched, "channelId", "id", "name"))
		}
	}
	if rows, ok := s.loadList("pairings", ""); !ok {
		checks = append(checks, unknown("pairing_target", "待处理 IM 配对", false))
	} else {
		matched := []any{}
		valid := true
		for _, r := range rows {
			values, e := targets(r)
			if e != nil {
				valid = false
				break
			}
			if values[target] {
				matched = append(matched, r)
			}
		}
		if !valid {
			checks = append(checks, unknown("pairing_target", "待处理 IM 配对", false))
		} else {
			c := matching("pairing_target", "待处理 IM 配对", "warning", false, matched, "pendingId", "id", "conversationName")
			c.Note = "待审批配对尚未形成活跃绑定，但删除后可能无法按原目标批准"
			checks = append(checks, c)
		}
	}
	if groups, ok := s.loadList("groups", ""); !ok {
		checks = append(checks, unknown("group_membership", "Group 成员与负责人", true))
	} else {
		valid := true
		matched := []any{}
		for _, r := range groups {
			group, members, leader, e := groupParts(r)
			if e != nil || memberID(leader) == "" {
				valid = false
				continue
			}
			hit := memberID(leader) == target
			for _, m := range members {
				if memberID(m) == "" {
					valid = false
				}
				hit = hit || memberID(m) == target
			}
			if hit {
				matched = append(matched, group)
			}
		}
		if !valid {
			checks = append(checks, unknown("group_membership", "Group 成员与负责人", true))
		} else {
			checks = append(checks, matching("group_membership", "Group 成员与负责人", "blocked", true, matched, "id", "groupId", "name", "title"))
		}
	}
	if workflows, ok := s.loadList("workflows", target); !ok {
		checks = append(checks, unknown("scoped_workflow", "Waker 作用域 Workflow", true))
	} else {
		checks = append(checks, matching("scoped_workflow", "Waker 作用域 Workflow", "blocked", true, workflows, "workflowId", "id", "title", "name"))
	}
	if sessions, ok := s.loadList("sessions", target); !ok {
		checks = append(checks, unknown("active_session", "运行中 Session", true), unknown("session_history", "历史 Session", false))
	} else {
		active, history := []any{}, []any{}
		valid := true
		for _, r := range sessions {
			state, e := sessionState(r)
			if e != nil {
				valid = false
				continue
			}
			if state == "active" {
				active = append(active, r)
			} else {
				history = append(history, r)
			}
		}
		if valid {
			checks = append(checks, matching("active_session", "运行中 Session", "blocked", true, active, "session_id", "sessionId", "id", "title"))
		} else {
			checks = append(checks, unknown("active_session", "运行中 Session", true))
		}
		c := matching("session_history", "历史 Session", "warning", false, history, "session_id", "sessionId", "id", "title")
		c.Note = "删除 Waker 可能使历史会话、产物和安装状态失去关联"
		checks = append(checks, c)
	}
	activity, e := s.Source("activity", "")
	a, valid := object(activity)
	hasTasks := false
	if e == nil && valid {
		for _, key := range []string{"runningConversationTasks", "runningTriggerTasks", "queuedTriggerTasks"} {
			n, ok := counter(a[key])
			if !ok {
				valid = false
			}
			hasTasks = hasTasks || n > 0
		}
	} else {
		valid = false
	}
	c := check("task_attribution", "运行/排队任务归因", "clear", true, 0, nil, "")
	if !valid || hasTasks {
		c = unknown("task_attribution", "运行/排队任务归因", true)
		if valid && hasTasks {
			c.Note = "daemon 报告存在任务，但当前接口不能完整归因到单个 Waker"
		}
	}
	checks = append(checks, c)
	skillRows, _ := list(row["skills"])
	c = check("installed_content_boundary", "插件、Skill 与外部脚本边界", "warning", false, len(skillRows), nil, "已安装内容和面板外脚本无法统一枚举；删除前需人工确认")
	c.Completeness = "partial"
	checks = append(checks, c)
	revision, e := fingerprint(detail)
	if e != nil {
		checks = append(checks, unknown("waker_revision", "Waker 资源版本", true))
	}
	return checks, revision
}
