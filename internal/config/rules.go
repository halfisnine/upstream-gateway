package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type StatusRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}
type Condition struct {
	Field    string   `json:"field"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}
type ConditionGroup struct {
	Conditions []Condition `json:"conditions"`
}
type Rule struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Enabled      bool             `json:"enabled"`
	Channels     []string         `json:"channels,omitempty"`
	Protocols    []string         `json:"protocols,omitempty"`
	Sources      []string         `json:"sources"`
	Statuses     []StatusRange    `json:"statuses,omitempty"`
	Groups       []ConditionGroup `json:"groups,omitempty"`
	IgnoreCase   bool             `json:"ignore_case"`
	ReturnStatus int              `json:"return_status,omitempty"`
	Message      string           `json:"message"`
}

func validText(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func ValidateRules(rules []Rule, channels []Channel) error {
	if len(rules) > 128 {
		return errors.New("too_many_error_rules")
	}
	known := map[string]bool{}
	for _, ch := range channels {
		known[ch.ID] = true
	}
	ids := map[string]bool{}
	for i, r := range rules {
		fail := func(s string) error { return fmt.Errorf("error_rules[%d].%s", i, s) }
		if !ValidID(r.ID) || ids[r.ID] {
			return fail("invalid_or_duplicate_id")
		}
		ids[r.ID] = true
		if !validText(r.Name, 100) || !validText(r.Message, 1024) {
			return fail("invalid_name_or_public_message")
		}
		if len(r.Channels) > 128 || len(r.Protocols) > 4 || len(r.Sources) == 0 || len(r.Sources) > 3 || len(r.Statuses) > 16 || len(r.Groups) > 8 {
			return fail("too_many_conditions")
		}
		for _, id := range r.Channels {
			if !known[id] {
				return fail("unknown_channel")
			}
		}
		for _, p := range r.Protocols {
			if !IsProtocol(p) {
				return fail("unsupported_protocol")
			}
		}
		for _, s := range r.Sources {
			if !slices.Contains([]string{"http", "json", "sse"}, s) {
				return fail("unsupported_source")
			}
		}
		for _, s := range r.Statuses {
			if s.From < 100 || s.To > 599 || s.To < s.From {
				return fail("invalid_status_range")
			}
		}
		if r.ReturnStatus != 0 && (r.ReturnStatus < 400 || r.ReturnStatus > 599) {
			return fail("invalid_return_status")
		}
		for _, g := range r.Groups {
			if len(g.Conditions) == 0 || len(g.Conditions) > 8 {
				return fail("invalid_condition_group")
			}
			for _, c := range g.Conditions {
				if !slices.Contains([]string{"code", "type", "message"}, c.Field) || !slices.Contains([]string{"exists", "equals", "contains"}, c.Operator) {
					return fail("unsupported_condition")
				}
				if c.Operator == "exists" {
					if len(c.Values) != 0 {
						return fail("exists_has_values")
					}
				} else if len(c.Values) == 0 || len(c.Values) > 16 {
					return fail("invalid_condition_values")
				}
				for _, v := range c.Values {
					if !validText(v, 256) {
						return fail("invalid_condition_value")
					}
				}
			}
		}
	}
	return nil
}
