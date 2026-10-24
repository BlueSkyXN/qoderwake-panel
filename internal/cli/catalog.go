// Package cli validates and executes the registered official command contracts.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	contracts "qoderwake-panel/panel/config"
)

type Argument struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Variadic    bool   `json:"variadic"`
	Description string `json:"description"`
}
type Option struct {
	Name          string   `json:"name"`
	Aliases       []string `json:"aliases"`
	Description   string   `json:"description"`
	Value         bool     `json:"value"`
	OptionalValue bool     `json:"optionalValue"`
	Variadic      bool     `json:"variadic"`
	Repeatable    bool     `json:"repeatable"`
	Placeholder   string   `json:"placeholder"`
	Choices       []string `json:"choices"`
	Secret        bool     `json:"secret"`
}
type Spec struct {
	ID          string     `json:"id"`
	Path        []string   `json:"path"`
	Group       string     `json:"group"`
	GroupName   string     `json:"groupName"`
	Description string     `json:"description"`
	Arguments   []Argument `json:"arguments"`
	Options     []Option   `json:"options"`
	Help        string     `json:"help"`
	Revision    string     `json:"revision"`
	Effect      string     `json:"effect"`
	Scope       string     `json:"scope"`
	PanelPage   *string    `json:"panelPage"`
	PanelOnly   bool       `json:"panelOnly"`
}
type Policy struct {
	Read      []string            `json:"read"`
	Scoped    map[string][]string `json:"scoped"`
	Pages     map[string]string   `json:"pages"`
	PanelOnly []string            `json:"panelOnly"`
	Spend     []string            `json:"spend"`
}
type Catalog struct {
	Version string
	Policy  Policy
	Specs   []Spec
	byID    map[string]int
}

var Secret = regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api.?key|authorization|cookie|private.?key)(?:$|[-_](?:file|path)$)`)
var groups = map[string]string{"start": "服务启动", "stop": "服务停止", "restart": "服务重启", "status": "服务状态", "readiness": "运行就绪", "portal": "官方控制台", "config": "应用配置", "proxy": "网络代理", "settings": "用户偏好", "storage": "存储迁移", "backup": "官方备份", "plugin": "本地插件", "waker": "Waker 与模板", "local": "本机员工", "project": "项目", "mcp": "MCP 服务器", "skill": "技能", "permission": "Waker 权限", "channel": "IM 通道与配对", "extension": "扩展运行时", "session": "会话", "group": "群协作", "sop": "协作规程", "messages": "会话消息", "atwaker": "IM 工作任务", "todo": "待办", "goal": "协作目标", "runs": "运行任务", "trace": "任务链路", "automation": "自动化", "memory": "记忆生命周期", "login": "官方登录", "logout": "官方退出", "whoami": "官方身份", "version": "版本", "update": "官方升级", "log": "日志", "feedback": "反馈报告", "teams": "团队", "workflow": "WakerFlow", "models": "模型", "account": "官方账号", "connector": "连接器", "board": "任务看板", "human-action": "人工处理", "knowledge": "知识库", "remote": "跨设备员工", "auth": "外部认证", "diagnostics": "诊断"}
var entryRE = regexp.MustCompile(`^  (\S.*?)\s{2,}(\S.*)$`)
var usageRE = regexp.MustCompile(`^Usage: \S+\s*(.*)`)
var placeholderRE = regexp.MustCompile(`(<[^>]+>|\[[^\]]+\])`)
var flagsRE = regexp.MustCompile(`(?:^|\s)(--?[A-Za-z0-9][A-Za-z0-9-]*)`)
var choiceRE = regexp.MustCompile(`choices: ((?:"[^"]+"(?:, )?)+)`)
var quotedRE = regexp.MustCompile(`"([^"]+)"`)
var repeatRE = regexp.MustCompile(`(?i)default: \[\]|repeatable|repeat for|can be repeated`)
var headingRE = regexp.MustCompile(`^\w+:`)

func helpEntries(value, heading string) [][2]string {
	active := false
	entries := [][2]string{}
	for _, line := range strings.Split(value, "\n") {
		if line == heading+":" {
			active = true
			continue
		}
		if !active {
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		m := entryRE.FindStringSubmatch(line)
		if m != nil {
			entries = append(entries, [2]string{m[1], m[2]})
		} else if strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "" {
			if strings.HasPrefix(line, "    ") && len(entries) > 0 {
				entries[len(entries)-1][1] += " " + strings.TrimSpace(line)
			} else {
				entries = append(entries, [2]string{strings.TrimSpace(line), ""})
			}
		}
	}
	return entries
}
func ParseHelp(path []string, help string) (Spec, error) {
	spec := Spec{ID: strings.Join(path, "."), Path: path, Arguments: []Argument{}, Options: []Option{}, Help: help}
	sum := sha256.Sum256([]byte(help))
	spec.Revision = hex.EncodeToString(sum[:])
	if len(path) > 0 {
		spec.Group = path[0]
		spec.GroupName = groups[path[0]]
		if spec.GroupName == "" {
			spec.GroupName = path[0]
		}
	}
	usage := usageRE.FindStringSubmatch(strings.Split(help, "\n")[0])
	suffix := ""
	if usage != nil {
		suffix = usage[1]
	}
	for _, part := range path {
		word, rest, _ := strings.Cut(suffix, " ")
		if !slices.Contains(strings.Split(word, "|"), part) {
			return Spec{}, apierror.Invalid("cli_help_mismatch")
		}
		suffix = strings.TrimLeft(rest, " \t")
	}
	descriptions := map[string]string{}
	for _, entry := range helpEntries(help, "Arguments") {
		descriptions[entry[0]] = entry[1]
	}
	for _, token := range placeholderRE.FindAllString(suffix, -1) {
		name := strings.TrimSuffix(token[1:len(token)-1], "...")
		if name == "options" || name == "command" {
			continue
		}
		spec.Arguments = append(spec.Arguments, Argument{name, strings.HasPrefix(token, "<"), strings.Contains(token, "..."), descriptions[name]})
	}
	for _, entry := range helpEntries(help, "Options") {
		flags := []string{}
		for _, match := range flagsRE.FindAllStringSubmatch(strings.ReplaceAll(entry[0], ",", " "), -1) {
			flags = append(flags, match[1])
		}
		if len(flags) == 0 || slices.Contains(flags, "--help") {
			continue
		}
		name := flags[0]
		for _, f := range flags {
			if strings.HasPrefix(f, "--") {
				name = f
				break
			}
		}
		value := placeholderRE.FindString(entry[0])
		placeholder := ""
		if value != "" {
			placeholder = strings.TrimSuffix(value[1:len(value)-1], "...")
		}
		choices := []string{}
		if match := choiceRE.FindStringSubmatch(entry[1]); match != nil {
			for _, c := range quotedRE.FindAllStringSubmatch(match[1], -1) {
				choices = append(choices, c[1])
			}
		}
		spec.Options = append(spec.Options, Option{Name: name, Aliases: flags, Description: entry[1], Value: value != "", OptionalValue: strings.HasPrefix(value, "["), Variadic: strings.Contains(value, "..."), Repeatable: repeatRE.MatchString(entry[1]), Placeholder: placeholder, Choices: choices, Secret: Secret.MatchString(name)})
	}
	paragraph := strings.Split(help, "\n\n")
	if len(paragraph) > 1 && !headingRE.MatchString(paragraph[1]) {
		spec.Description = strings.TrimSpace(paragraph[1])
	}
	return spec, nil
}
func NewCatalog() (*Catalog, error) {
	var reference struct {
		Version string `json:"version"`
		Nodes   []struct {
			Path []string `json:"path"`
			Help string   `json:"help"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(contracts.Reference, &reference); err != nil {
		return nil, err
	}
	c := &Catalog{Version: reference.Version, Specs: []Spec{}, byID: map[string]int{}}
	if err := json.Unmarshal(contracts.Policy, &c.Policy); err != nil {
		return nil, err
	}
	for _, node := range reference.Nodes {
		if len(node.Path) == 0 || len(helpEntries(node.Help, "Commands")) > 0 {
			continue
		}
		spec, err := ParseHelp(node.Path, node.Help)
		if err != nil {
			return nil, err
		}
		spec.Effect = "write"
		if slices.Contains(c.Policy.Read, spec.ID) {
			spec.Effect = "read"
		}
		spec.Scope = "admin"
		if _, ok := c.Policy.Scoped[spec.ID]; ok {
			spec.Scope = "waker"
		}
		if page, ok := c.Policy.Pages[spec.Group]; ok {
			spec.PanelPage = &page
		}
		spec.PanelOnly = slices.Contains(c.Policy.PanelOnly, spec.ID)
		c.byID[spec.ID] = len(c.Specs)
		c.Specs = append(c.Specs, spec)
	}
	return c, nil
}
func (c *Catalog) Get(id string) (Spec, error) {
	index, ok := c.byID[id]
	if !ok {
		return Spec{}, apierror.New(404, "cli_operation_not_found")
	}
	return c.Specs[index], nil
}
func (c *Catalog) Visible(identity auth.Identity, spec Spec) bool {
	return identity.Role == "admin" || (identity.UserID != "" && spec.Scope == "waker" && (spec.Effect == "read" || identity.Role == "maintainer"))
}

type Parameters struct {
	Arguments map[string]any `json:"arguments"`
	Options   map[string]any `json:"options"`
}

func (c *Catalog) Authorize(identity auth.Identity, spec Spec, params *Parameters) error {
	if !c.Visible(identity, spec) {
		return apierror.New(403, "permission_denied")
	}
	if params == nil || identity.Role == "admin" {
		return nil
	}
	allowed := c.Policy.Scoped[spec.ID]
	for key := range params.Options {
		if !slices.Contains(allowed, key) {
			return apierror.New(403, "cli_scoped_option_rejected")
		}
	}
	wid, ok := params.Options["--waker-id"].(string)
	if !ok || !slices.Contains(identity.Wakers, wid) {
		return apierror.New(403, "waker_scope_rejected")
	}
	if value, exists := params.Options["--path"]; exists {
		path, ok := value.(string)
		if !ok || filepath.IsAbs(path) || slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
			return apierror.New(403, "cli_scoped_option_rejected")
		}
	}
	return nil
}
func (c *Catalog) Listing(identity auth.Identity) map[string]any {
	out := []map[string]any{}
	for _, spec := range c.Specs {
		if c.Visible(identity, spec) {
			b, _ := json.Marshal(spec)
			row := map[string]any{}
			json.Unmarshal(b, &row)
			delete(row, "options")
			delete(row, "arguments")
			delete(row, "help")
			out = append(out, row)
		}
	}
	return map[string]any{"referenceVersion": c.Version, "items": out}
}
func parameterString(value any) (string, error) {
	s, ok := value.(string)
	if !ok || strings.ContainsRune(s, 0) || len(s) > 60000 {
		return "", apierror.Invalid("invalid_cli_parameter")
	}
	return s, nil
}
func BuildArgv(spec Spec, values Parameters) ([]string, error) {
	optionNames := map[string]bool{}
	argNames := map[string]bool{}
	for _, o := range spec.Options {
		optionNames[o.Name] = true
	}
	for _, a := range spec.Arguments {
		argNames[a.Name] = true
	}
	for k := range values.Options {
		if !optionNames[k] {
			return nil, apierror.Invalid("unknown_cli_parameter")
		}
	}
	for k := range values.Arguments {
		if !argNames[k] {
			return nil, apierror.Invalid("unknown_cli_parameter")
		}
	}
	result := append([]string{}, spec.Path...)
	for _, option := range spec.Options {
		value, exists := values.Options[option.Name]
		if !exists {
			continue
		}
		if !option.Value {
			b, ok := value.(bool)
			if !ok {
				return nil, apierror.Invalid("invalid_cli_boolean")
			}
			if b {
				result = append(result, option.Name)
			}
			continue
		}
		if option.OptionalValue && value == true {
			result = append(result, option.Name)
			continue
		}
		vals, ok := value.([]any)
		if !ok {
			vals = []any{value}
		}
		if len(vals) < 1 || len(vals) > 200 {
			return nil, apierror.Invalid("invalid_cli_parameter")
		}
		for i, v := range vals {
			item, err := parameterString(v)
			if err != nil {
				return nil, err
			}
			if len(option.Choices) > 0 && !slices.Contains(option.Choices, item) {
				return nil, apierror.Invalid("invalid_cli_choice")
			}
			if i == 0 || !option.Variadic {
				result = append(result, option.Name+"="+item)
			} else {
				if strings.HasPrefix(item, "-") {
					return nil, apierror.Invalid("invalid_cli_variadic_value")
				}
				result = append(result, item)
			}
		}
	}
	positionals := []string{}
	skipped := false
	for _, arg := range spec.Arguments {
		value, exists := values.Arguments[arg.Name]
		if !exists || value == nil {
			if arg.Required {
				return nil, apierror.Invalid("cli_argument_required")
			}
			skipped = true
			continue
		}
		if skipped {
			return nil, apierror.Invalid("cli_argument_gap")
		}
		vals := []any{value}
		if arg.Variadic {
			var ok bool
			vals, ok = value.([]any)
			if !ok {
				return nil, apierror.Invalid("invalid_cli_parameter")
			}
		}
		if len(vals) < 1 || len(vals) > 200 {
			return nil, apierror.Invalid("invalid_cli_parameter")
		}
		for _, v := range vals {
			s, err := parameterString(v)
			if err != nil {
				return nil, err
			}
			positionals = append(positionals, s)
		}
	}
	if len(positionals) > 0 {
		result = append(result, "--")
		result = append(result, positionals...)
	}
	return result, nil
}
