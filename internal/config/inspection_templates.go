package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Matchers operate on the path after /r/<channel>, without the query string.
type PathMatch struct {
	Method string `json:"method"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
}

type InspectionTemplate struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Protocol     string      `json:"protocol"`
	Mode         string      `json:"mode"` // json_sse or http_errors
	HTTPStatuses *string     `json:"http_statuses,omitempty"`
	Matches      []PathMatch `json:"matches"`
	Exclude      []PathMatch `json:"exclude,omitempty"`
}

type compiledPath struct {
	PathMatch
	re *regexp.Regexp
}
type CompiledTemplate struct {
	Protocol, Mode        string
	matches, exclude      []compiledPath
	statuses              []StatusRange
	LegacyStatusSelection bool
}

func compilePaths(paths []PathMatch) ([]compiledPath, error) {
	var result []compiledPath
	seen := map[PathMatch]bool{}
	for _, m := range paths {
		if !ValidAllowedMethod(m.Method) || len(m.Path) == 0 || len(m.Path) > 1024 || seen[m] {
			return nil, errors.New("invalid_template_match")
		}
		seen[m] = true
		p := compiledPath{PathMatch: m}
		switch m.Kind {
		case "exact", "prefix":
			if !ValidAllowedPath(m.Path) {
				return nil, errors.New("invalid_template_path")
			}
		case "regex":
			var err error
			p.re, err = regexp.Compile("^(?:" + m.Path + ")$")
			if err != nil {
				return nil, errors.New("invalid_template_regex")
			}
		default:
			return nil, errors.New("invalid_template_match_kind")
		}
		result = append(result, p)
	}
	return result, nil
}

func CompileTemplate(t InspectionTemplate) (CompiledTemplate, error) {
	if !ValidID(t.ID) || strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 || !IsProtocol(t.Protocol) || (t.Mode != "json_sse" && t.Mode != "http_errors") || len(t.Matches) == 0 || len(t.Matches) > 32 || len(t.Exclude) > 64 {
		return CompiledTemplate{}, errors.New("invalid_inspection_template")
	}
	statuses, err := ParseInspectionStatuses(t.StatusExpression())
	if err != nil {
		return CompiledTemplate{}, err
	}
	m, err := compilePaths(t.Matches)
	if err != nil {
		return CompiledTemplate{}, err
	}
	e, err := compilePaths(t.Exclude)
	if err != nil {
		return CompiledTemplate{}, err
	}
	return CompiledTemplate{Protocol: t.Protocol, Mode: t.Mode, matches: m, exclude: e, statuses: statuses, LegacyStatusSelection: t.HTTPStatuses == nil}, nil
}

func matchesPath(rules []compiledPath, method, path string) bool {
	for _, r := range rules {
		if r.Method != method {
			continue
		}
		if (r.Kind == "exact" && path == r.Path) || (r.Kind == "prefix" && strings.HasPrefix(path, r.Path)) || (r.re != nil && r.re.MatchString(path)) {
			return true
		}
	}
	return false
}
func (t CompiledTemplate) Match(method, path string) bool {
	return matchesPath(t.matches, method, path) && !matchesPath(t.exclude, method, path)
}

// An omitted field retains the initial schema 7 behavior; an explicit empty
// string disables HTTP-status checks without disabling optional body checks.
func (t InspectionTemplate) StatusExpression() string {
	if t.HTTPStatuses != nil {
		return *t.HTTPStatuses
	}
	if t.Mode == "json_sse" {
		return "201-599"
	}
	return "400-599"
}

var inspectionStatusItem = regexp.MustCompile(`^([0-9]{3})(?:\s*[-–]\s*([0-9]{3}))?$`)

func ParseInspectionStatuses(value string) ([]StatusRange, error) {
	fail := func() ([]StatusRange, error) { return nil, errors.New("invalid_inspection_statuses") }
	if len(value) > 512 {
		return fail()
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(strings.ReplaceAll(value, "，", ","), ",")
	if len(parts) > 32 {
		return fail()
	}
	result := make([]StatusRange, 0, len(parts))
	for _, part := range parts {
		match := inspectionStatusItem.FindStringSubmatch(strings.TrimSpace(part))
		if match == nil {
			return fail()
		}
		from, _ := strconv.Atoi(match[1])
		to := from
		if match[2] != "" {
			to, _ = strconv.Atoi(match[2])
		}
		if from < 200 || to > 599 || from > to {
			return fail()
		}
		result = append(result, StatusRange{From: from, To: to})
	}
	return result, nil
}
func (t CompiledTemplate) ChecksStatus(status int) bool {
	for _, r := range t.statuses {
		if status >= r.From && status <= r.To {
			return true
		}
	}
	return false
}

// Presets are copied into versioned configuration; changing the catalog does
// not change a previously published template or its channel associations.
func InspectionPresets() []InspectionTemplate {
	makeTemplate := func(id, name, protocol, mode string, paths ...string) InspectionTemplate {
		status := "400-599"
		t := InspectionTemplate{ID: id, Name: name, Protocol: protocol, Mode: mode, HTTPStatuses: &status}
		for _, p := range paths {
			t.Matches = append(t.Matches, PathMatch{"POST", "exact", p})
		}
		return t
	}
	gemini := makeTemplate("gemini-generation", "Gemini 内容生成", "gemini", "json_sse")
	gemini.Matches = []PathMatch{{"POST", "regex", `/v1(beta)?/models/[A-Za-z0-9._-]+:(generateContent|streamGenerateContent)`}}
	return []InspectionTemplate{
		makeTemplate("openai-chat", "OpenAI Chat", "openai_chat", "json_sse", "/v1/chat/completions"),
		makeTemplate("openai-responses", "OpenAI Responses", "openai_responses", "json_sse", "/v1/responses"),
		makeTemplate("anthropic-messages", "Anthropic Messages", "anthropic_messages", "json_sse", "/v1/messages"),
		gemini,
		makeTemplate("openai-images", "OpenAI 图片接口", "openai_chat", "http_errors", "/v1/images/generations", "/v1/images/edits", "/v1/images/variations"),
		makeTemplate("openai-audio", "OpenAI 音频接口", "openai_chat", "http_errors", "/v1/audio/speech", "/v1/audio/transcriptions", "/v1/audio/translations"),
		makeTemplate("openai-embeddings", "OpenAI Embeddings", "openai_chat", "http_errors", "/v1/embeddings"),
		makeTemplate("rerank", "Rerank", "openai_chat", "http_errors", "/v1/rerank", "/rerank"),
		makeTemplate("anthropic-count-tokens", "Anthropic Token 计数", "anthropic_messages", "http_errors", "/v1/messages/count_tokens"),
	}
}

// Old snapshots keep the old router at runtime. This conversion is an editable
// proposal only; publishing it explicitly switches to default passthrough.
func migrateInspection(c Config) Config {
	c.LegacyRouting = false
	c.InspectionTemplates = InspectionPresets()
	for i := range c.Channels {
		ch := &c.Channels[i]
		ch.InspectionTemplates = nil
		for _, preset := range InspectionPresets()[:4] {
			if _, ok := ch.Protocols[preset.Protocol]; !ok {
				continue
			}
			// Preserve the former non-200 scope in the editable migration.
			legacyStatuses := "201-599"
			preset.HTTPStatuses = &legacyStatuses
			for j := range c.InspectionTemplates {
				if c.InspectionTemplates[j].ID == preset.ID {
					c.InspectionTemplates[j].HTTPStatuses = &legacyStatuses
				}
			}
			compiled, _ := CompileTemplate(preset)
			for _, old := range ch.PathAllowlist {
				if compiled.Match(old.Method, old.Path) {
					preset.Exclude = append(preset.Exclude, PathMatch{old.Method, "exact", old.Path})
				}
			}
			if len(preset.Exclude) > 0 {
				preset.ID = "legacy-" + strconv.Itoa(i) + "-" + preset.ID
				preset.Name = ch.Name + " · " + preset.Name
				if len(preset.Name) > 100 {
					preset.Name = "旧配置转换 · " + preset.ID
				}
				c.InspectionTemplates = append(c.InspectionTemplates, preset)
			}
			ch.InspectionTemplates = append(ch.InspectionTemplates, preset.ID)
		}
		ch.PathAllowlist = nil
	}
	return c
}
