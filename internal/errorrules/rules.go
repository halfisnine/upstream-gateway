// Package errorrules matches only extracted error fields, never model output.
package errorrules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/halfisnine/upstream-gateway/internal/config"
)

const DefaultMessage = "Request could not be completed."

type Error struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}
type Sample struct {
	Channel   string `json:"channel"`
	Protocol  string `json:"protocol"`
	Source    string `json:"source"`
	Status    int    `json:"status"`
	Committed bool   `json:"committed"`
	Error     Error  `json:"error"`
}
type Decision struct {
	RuleID  string `json:"rule_id"`
	Group   int    `json:"group"`
	Status  int    `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

func Match(rules []config.Rule, s Sample) Decision {
	status := s.Status
	if status < 400 || status > 599 {
		status = 502
	}
	fallback := Decision{RuleID: "", Status: status, Message: DefaultMessage, Reason: "unmatched_error"}
	// Channel-specific rules precede common rules. Preserve order in each group.
	for _, specific := range []bool{true, false} {
		for _, r := range rules {
			if !r.Enabled || (len(r.Channels) > 0) != specific || (specific && !slices.Contains(r.Channels, s.Channel)) || (len(r.Protocols) > 0 && !slices.Contains(r.Protocols, s.Protocol)) || !slices.Contains(r.Sources, s.Source) {
				continue
			}
			if len(r.Statuses) > 0 && !slices.ContainsFunc(r.Statuses, func(v config.StatusRange) bool { return s.Status >= v.From && s.Status <= v.To }) {
				continue
			}
			group := 0
			for i, g := range r.Groups {
				all := true
				for _, c := range g.Conditions {
					if !matches(c, r.IgnoreCase, s.Error) {
						all = false
						break
					}
				}
				if all {
					group = i + 1
					break
				}
			}
			if len(r.Groups) > 0 && group == 0 {
				continue
			}
			if r.ReturnStatus != 0 {
				status = r.ReturnStatus
			}
			return Decision{RuleID: r.ID, Group: group, Status: status, Message: r.Message, Reason: "rule_matched"}
		}
	}
	return fallback
}
func matches(c config.Condition, fold bool, e Error) bool {
	var field string
	switch c.Field {
	case "code":
		field = e.Code
	case "type":
		field = e.Type
	case "message":
		field = e.Message
	}
	if c.Operator == "exists" {
		return field != ""
	}
	if fold {
		field = strings.ToLower(field)
	}
	for _, v := range c.Values {
		if fold {
			v = strings.ToLower(v)
		}
		if c.Operator == "equals" && field == v || c.Operator == "contains" && strings.Contains(field, v) {
			return true
		}
	}
	return false
}

// rawObject retains nested values as bytes rather than allocating the entire
// model response tree. Duplicate envelope keys fail closed.
func rawObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("invalid_error_document")
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("invalid_error_document")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate_error_field")
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	return result, nil
}
func scalar(raw json.RawMessage) string {
	if len(raw) > maxFieldBytes {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return string(n)
	}
	return ""
}

const maxFieldBytes = 64 << 10

// Extract recognizes top-level protocol errors and Responses failure envelopes.
// Nested model output and content blocks never participate in matching.
func Extract(raw []byte, protocol, event string) (Error, bool, error) {
	if !json.Valid(raw) {
		return Error{}, false, fmt.Errorf("invalid_error_document")
	}
	root, err := rawObject(raw)
	if err != nil {
		return Error{}, false, err
	}
	value, has := root["error"]
	typ := scalar(root["type"])
	if protocol == "openai_responses" && (typ == "response.failed" || event == "response.failed" || scalar(root["status"]) == "failed") {
		if responseRaw, ok := root["response"]; ok {
			response, e := rawObject(responseRaw)
			if e != nil {
				return Error{}, false, e
			}
			value = response["error"]
		}
		has = true
	}
	explicit := typ == "error" || event == "error"
	if !explicit && (!has || bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
		return Error{}, false, nil
	}
	fields := map[string]json.RawMessage{}
	if len(value) > 0 && bytes.HasPrefix(bytes.TrimSpace(value), []byte("{")) {
		fields, err = rawObject(value)
		if err != nil {
			return Error{}, false, err
		}
	} else if explicit {
		fields = root
	}
	result := Error{Code: scalar(fields["code"]), Type: scalar(fields["type"]), Message: scalar(fields["message"])}
	if result.Type == "" {
		result.Type = scalar(fields["status"])
	}
	if result.Message == "" {
		result.Message = scalar(value)
	}
	return result, true, nil
}
func HTTPFields(raw []byte, protocol string) Error {
	if e, ok, err := Extract(raw, protocol, ""); err == nil && ok {
		return e
	}
	if root, err := rawObject(raw); err == nil {
		return Error{Code: scalar(root["code"]), Type: scalar(root["type"]), Message: scalar(root["message"])}
	}
	return Error{Message: string(raw)}
}
func Presets() []config.Rule {
	return []config.Rule{
		{ID: "balance", Name: "上游余额不足", Enabled: true, Sources: []string{"http", "json", "sse"}, Statuses: []config.StatusRange{{From: 200, To: 200}, {From: 402, To: 403}, {From: 429, To: 429}}, IgnoreCase: true, ReturnStatus: 503, Message: "上游服务暂时不可用，请联系管理员。", Groups: []config.ConditionGroup{{Conditions: []config.Condition{{Field: "code", Operator: "equals", Values: []string{"insufficient_quota", "insufficient_balance"}}}}, {Conditions: []config.Condition{{Field: "message", Operator: "contains", Values: []string{"余额不足", "insufficient balance", "credit balance is too low"}}}}}},
		{ID: "authentication", Name: "上游认证失效", Enabled: true, Sources: []string{"http", "json", "sse"}, Statuses: []config.StatusRange{{From: 401, To: 401}, {From: 403, To: 403}}, IgnoreCase: true, ReturnStatus: 502, Message: "上游服务认证异常，请联系管理员。", Groups: []config.ConditionGroup{{Conditions: []config.Condition{{Field: "code", Operator: "equals", Values: []string{"invalid_api_key", "authentication_error"}}}}, {Conditions: []config.Condition{{Field: "message", Operator: "contains", Values: []string{"invalid api key", "认证失败"}}}}}},
		{ID: "rate-limit", Name: "上游请求限流", Enabled: true, Sources: []string{"http", "json", "sse"}, Statuses: []config.StatusRange{{From: 429, To: 429}}, Message: "上游请求繁忙，请稍后再试。"},
		{ID: "unavailable", Name: "上游服务不可用", Enabled: true, Sources: []string{"http", "json", "sse"}, Statuses: []config.StatusRange{{From: 500, To: 599}}, ReturnStatus: 503, Message: "上游服务暂时不可用，请稍后再试。"},
	}
}
