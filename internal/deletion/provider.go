package deletion

import "strings"

func boundary(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("_.:/-", rune(c))
}
func workflowMatch(value any, used stringSet) (bool, error) {
	row, ok := object(value)
	if !ok {
		return false, unavailable
	}
	structured, e := models(row)
	if e != nil {
		return false, e
	}
	if intersects(used, structured) {
		return true, nil
	}
	for _, key := range []string{"description", "script"} {
		value, ok := row[key].(string)
		if !ok {
			continue
		}
		for model := range used {
			start := 0
			for {
				index := strings.Index(value[start:], model)
				if index < 0 {
					break
				}
				index += start
				end := index + len(model)
				if (index == 0 || !boundary(value[index-1])) && (end == len(value) || !boundary(value[end])) {
					return true, nil
				}
				start = index + 1
				if start >= len(value) {
					break
				}
			}
		}
	}
	return false, nil
}
func (s Scanner) provider(target string) []Check {
	raw, e := s.Source("provider_models", target)
	modelRows, ok := list(raw)
	if e != nil || !ok || len(modelRows) == 0 {
		return []Check{unknown("provider_models", "Provider 模型集合", true)}
	}
	used := set()
	for _, m := range modelRows {
		if text(m) == "" {
			return []Check{unknown("provider_models", "Provider 模型集合", true)}
		}
		used[text(m)] = true
	}
	checks := []Check{}
	wakers, wakersOK := s.loadList("wakers", "")
	if !wakersOK {
		checks = append(checks, unknown("model_preference", "Waker 模型偏好", true))
	} else {
		matched := []any{}
		valid := true
		for _, w := range wakers {
			wid := singleTarget(w)
			if wid == "" {
				valid = false
				continue
			}
			value, e := s.Source("preference", wid)
			if e != nil {
				valid = false
				continue
			}
			refs, e := preferences(value)
			if e != nil {
				valid = false
				continue
			}
			if intersects(used, refs) {
				matched = append(matched, w)
			}
		}
		if !valid {
			checks = append(checks, unknown("model_preference", "Waker 模型偏好", true))
		} else {
			checks = append(checks, matching("model_preference", "Waker 模型偏好", "blocked", true, matched, "agentId", "id", "name"))
		}
	}
	for _, source := range []struct {
		name, id, label string
		keys            []string
	}{{"triggers", "trigger_model", "Trigger 模型", []string{"triggerId", "id", "triggerName"}}, {"channels", "channel_model", "Channel 模型", []string{"channelId", "id", "name"}}, {"pairings", "pairing_model", "配对目标模型", []string{"pendingId", "id", "conversationName"}}} {
		rows, ok := s.loadList(source.name, "")
		if !ok {
			checks = append(checks, unknown(source.id, source.label, true))
			continue
		}
		valid := true
		matched := []any{}
		for _, r := range rows {
			value := r
			if source.name == "channels" {
				data, e := channelPayload(r)
				if e != nil {
					valid = false
					continue
				}
				value = data
			}
			refs, e := models(value)
			if e != nil {
				valid = false
				continue
			}
			if intersects(used, refs) {
				matched = append(matched, r)
			}
		}
		if !valid {
			checks = append(checks, unknown(source.id, source.label, true))
		} else {
			checks = append(checks, matching(source.id, source.label, "blocked", true, matched, source.keys...))
		}
	}
	if groups, ok := s.loadList("groups", ""); !ok {
		checks = append(checks, unknown("group_model", "Group 成员模型", true))
	} else {
		valid := true
		matched := []any{}
		for _, r := range groups {
			group, members, leader, e := groupParts(r)
			if e != nil {
				valid = false
				continue
			}
			all := append(append([]any{}, members...), leader)
			hit := false
			for _, m := range all {
				refs, e := memberModels(m)
				if e != nil {
					valid = false
					continue
				}
				hit = hit || intersects(used, refs)
			}
			if hit {
				matched = append(matched, group)
			}
		}
		if !valid {
			checks = append(checks, unknown("group_model", "Group 成员模型", true))
		} else {
			checks = append(checks, matching("group_model", "Group 成员模型", "blocked", true, matched, "id", "groupId", "name", "title"))
		}
	}
	active, history := []any{}, []any{}
	sessionsValid := wakersOK
	if wakersOK {
		for _, w := range wakers {
			wid := singleTarget(w)
			if wid == "" {
				sessionsValid = false
				continue
			}
			sessions, ok := s.loadList("sessions", wid)
			if !ok {
				sessionsValid = false
				continue
			}
			for _, r := range sessions {
				state, e := sessionState(r)
				if e != nil {
					sessionsValid = false
					continue
				}
				row, _ := object(r)
				value, exists := row["currentModel"]
				if !exists {
					value = row["current_model"]
				}
				name, e := model(value)
				if e != nil {
					if state == "active" {
						sessionsValid = false
					}
					continue
				}
				if state == "active" {
					if name == "" {
						sessionsValid = false
					} else if used[name] {
						active = append(active, r)
					}
				} else if used[name] {
					history = append(history, r)
				}
			}
		}
	}
	if !sessionsValid {
		checks = append(checks, unknown("active_session_model", "运行中 Session 当前模型", true))
	} else {
		checks = append(checks, matching("active_session_model", "运行中 Session 当前模型", "blocked", true, active, "session_id", "sessionId", "id", "title"))
	}
	c := matching("historical_session_model", "历史 Session 模型", "warning", false, history, "session_id", "sessionId", "id", "title")
	c.Completeness = "partial"
	c.Note = "历史记录可能保留模型标识；缺失字段不能证明没有引用"
	checks = append(checks, c)
	scopes := []string{""}
	valid := true
	if wakersOK {
		for _, w := range wakers {
			wid := singleTarget(w)
			if wid == "" {
				valid = false
			} else {
				scopes = append(scopes, wid)
			}
		}
	}
	workflows := []any{}
	for _, scope := range scopes {
		values, ok := s.loadList("workflows", scope)
		if !ok {
			valid = false
		} else {
			workflows = append(workflows, values...)
		}
	}
	if !valid {
		checks = append(checks, unknown("workflow_text_reference", "Workflow 文本引用", false))
	} else {
		matched := []any{}
		for _, r := range workflows {
			hit, e := workflowMatch(r, used)
			if e != nil {
				valid = false
				continue
			}
			if hit {
				matched = append(matched, r)
			}
		}
		if !valid {
			checks = append(checks, unknown("workflow_text_reference", "Workflow 文本引用", false))
		} else {
			c = matching("workflow_text_reference", "Workflow 文本引用", "warning", false, matched, "workflowId", "id", "title", "name")
			c.Completeness = "partial"
			c.Note = "仅检查结构化字段和带边界的脚本文本标识；动态引用无法证明不存在"
			checks = append(checks, c)
		}
	}
	c = check("external_reference_boundary", "外部脚本与动态引用边界", "warning", false, 0, nil, "Panel 无法枚举面板外脚本、环境变量或运行时拼接的模型标识")
	c.Completeness = "partial"
	return append(checks, c)
}
