package errorrules

import (
	"github.com/halfisnine/upstream-gateway/internal/config"
	"testing"
)

func TestMatchingScopesAndConditions(t *testing.T) {
	common := Presets()[0]
	specific := common
	specific.ID = "specific"
	specific.Channels = []string{"a"}
	specific.ReturnStatus = 502
	rules := []config.Rule{common, specific}
	s := Sample{Channel: "a", Protocol: "openai_chat", Source: "http", Status: 403, Error: Error{Message: "CREDIT BALANCE IS TOO LOW"}}
	d := Match(rules, s)
	if d.RuleID != "specific" || d.Status != 502 || d.Group != 2 {
		t.Fatal(d)
	}
	s.Channel = "b"
	if Match(rules, s).RuleID != "balance" {
		t.Fatal("channel scope")
	}
	s.Error = Error{Code: "insufficient_quota"}
	if Match(rules, s).Group != 1 {
		t.Fatal("OR groups")
	}
	rules[0].Groups[0].Conditions = append(rules[0].Groups[0].Conditions, config.Condition{Field: "type", Operator: "exists"})
	if Match(rules, s).RuleID != "" {
		t.Fatal("AND constraints ignored")
	}
	s.Status = 200
	s.Source = "json"
	s.Error = Error{Message: "余额不足"}
	if Match(rules, s).Status != 503 {
		t.Fatal("embedded error")
	}
	s.Source = "sse"
	rules[0].Enabled = false
	if Match(rules, s).Status != 502 {
		t.Fatal("fallback")
	}
}
func TestExtractDoesNotScanNormalContent(t *testing.T) {
	for _, raw := range []string{`{"choices":[{"message":{"content":"余额不足 error insufficient_quota"}}],"usage":{"total_tokens":3}}`, `{"type":"content_block_delta","delta":{"text":"error"}}`, `{"error":null,"status":"completed"}`, `{"candidates":[{"content":{"parts":[{"text":"{\"error\":\"余额不足\"}"}]}}]}`} {
		if _, found, e := Extract([]byte(raw), "openai_chat", ""); found || e != nil {
			t.Fatalf("normal content changed: %s", raw)
		}
	}
	for _, tc := range []struct{ p, event, raw string }{{"openai_chat", "", `{"error":{"code":"insufficient_quota","message":"private"}}`}, {"anthropic_messages", "error", `{"type":"error","error":{"type":"overloaded_error","message":"private"}}`}, {"gemini", "", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"private"}}`}, {"openai_responses", "response.failed", `{"type":"response.failed","response":{"error":{"code":"failed","message":"private"}}}`}} {
		e, found, err := Extract([]byte(tc.raw), tc.p, tc.event)
		if err != nil || !found || e.Message != "private" {
			t.Fatal(tc, e, found, err)
		}
	}
}
func TestPresetValidation(t *testing.T) {
	if e := config.ValidateRules(Presets(), nil); e != nil {
		t.Fatal(e)
	}
}
func BenchmarkRuleMatch(b *testing.B) {
	r := Presets()
	s := Sample{Channel: "a", Protocol: "openai_chat", Source: "http", Status: 403, Error: Error{Message: "credit balance is too low"}}
	b.ReportAllocs()
	for b.Loop() {
		Match(r, s)
	}
}
