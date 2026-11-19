package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestAllowedPathsValidationAndSnapshots(t *testing.T) {
	base := AllowedPath{Method: "GET", Path: "/v1/models", Protocol: "openai_chat"}
	c := valid()
	c.LegacyRouting = true
	c.Channels[0].PathAllowlist = []AllowedPath{base}
	key := bytes.Repeat([]byte{7}, 32)
	c.Schema = 6
	raw, _ := json.Marshal(c)
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	nonce := make([]byte, g.NonceSize())
	data := g.Seal(nonce, nonce, raw, []byte("upstream-gateway-schema-6"))
	sum := sha256.Sum256(data)
	got, err := Open(data, hex.EncodeToString(sum[:]), key)
	if err != nil || !got.LegacyRouting || got.Channels[0].PathAllowlist[0] != base {
		t.Fatal("legacy routing lost", err)
	}
	converted, source, err := OpenForEdit(data, hex.EncodeToString(sum[:]), key)
	if err != nil || source != 6 || converted.LegacyRouting || len(converted.Channels[0].PathAllowlist) != 0 || len(converted.Channels[0].InspectionTemplates) != 1 {
		t.Fatal("editable conversion failed", err)
	}
	if _, _, err = Seal(converted, key); err != nil {
		t.Fatal(err)
	}
	c.Schema = Schema
	for _, path := range []string{"", "v1/models", "https://elsewhere/v1/models", "/v1/*", "/v1/../models", "/v1/./models", "/v1//models", "/v1/%2e%2e/models", "/v1/models?key=x", "/v1/models#part", "/v1/模型", "/v1/\nmodels", "/v1/\\models"} {
		c.Channels[0].PathAllowlist[0] = base
		c.Channels[0].PathAllowlist[0].Path = path
		if Validate(c) == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	for _, method := range []string{"get", "CONNECT", "TRACE", "*", ""} {
		c.Channels[0].PathAllowlist[0] = base
		c.Channels[0].PathAllowlist[0].Method = method
		if Validate(c) == nil {
			t.Fatalf("accepted method %q", method)
		}
	}
	c.Channels[0].PathAllowlist = []AllowedPath{base, base}
	if Validate(c) == nil {
		t.Fatal("duplicate accepted")
	}
	c.Channels[0].PathAllowlist = []AllowedPath{base}
	c.Channels[0].PathAllowlist[0].Protocol = "gemini"
	if Validate(c) == nil {
		t.Fatal("disabled protocol accepted")
	}
	c.Channels[0].PathAllowlist = make([]AllowedPath, 65)
	if Validate(c) == nil {
		t.Fatal("oversize allowlist accepted")
	}
}

func TestSchemaFiveUpgradePreservesLimitsWithoutEnablingPassthrough(t *testing.T) {
	c := valid()
	c.Schema = 5
	c.TrafficLimits = TrafficLimits{Node: 1024, Upstream: 128}
	c.InspectEmbeddedErrors = true
	c.ErrorRules = []Rule{{ID: "balance", Name: "balance", Enabled: true, Sources: []string{"http"}, Message: "safe"}}
	raw, _ := json.Marshal(c)
	key := bytes.Repeat([]byte{9}, 32)
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	nonce := make([]byte, g.NonceSize())
	data := g.Seal(nonce, nonce, raw, []byte("upstream-gateway-schema-5"))
	sum := sha256.Sum256(data)
	got, err := Open(data, hex.EncodeToString(sum[:]), key)
	if err != nil || got.Schema != Schema || got.TrafficLimits != c.TrafficLimits || !got.InspectEmbeddedErrors || len(got.ErrorRules) != 1 || len(got.Channels[0].PathAllowlist) != 0 {
		t.Fatal("schema 5 upgrade changed existing settings", got, err)
	}
}

func TestInspectionTemplateMatchingAndReferences(t *testing.T) {
	c := valid()
	template := InspectionTemplate{ID: "custom", Name: "共享模板", Protocol: "openai_chat", Mode: "http_errors", Matches: []PathMatch{{"POST", "prefix", "/custom/"}, {"GET", "regex", `/catalog/[a-z]+`}}, Exclude: []PathMatch{{"POST", "exact", "/custom/health"}}}
	c.InspectionTemplates = []InspectionTemplate{template}
	c.Channels[0].InspectionTemplates = []string{"custom"}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{{"POST", "/custom/run", true}, {"POST", "/custom/health", false}, {"GET", "/custom/run", false}, {"GET", "/catalog/abc", true}, {"GET", "/catalog/abc/extra", false}, {"GET", "/prefix/catalog/abc", false}} {
		if compiled.Match(tc.method, tc.path) != tc.want {
			t.Fatal(tc)
		}
	}
	key := bytes.Repeat([]byte{1}, 32)
	data, digest, err := Seal(c, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, digest, key)
	if err != nil || got.LegacyRouting || got.InspectionTemplates[0].Matches[1] != template.Matches[1] || got.Channels[0].InspectionTemplates[0] != "custom" {
		t.Fatal("template snapshot lost", err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.InspectionTemplates[0].Matches[0].Kind = "unknown" },
		func(c *Config) { c.InspectionTemplates[0].Matches[0] = PathMatch{"POST", "regex", "(?=bad)"} },
		func(c *Config) { c.InspectionTemplates[0].Matches[0].Method = "CONNECT" },
		func(c *Config) { c.InspectionTemplates[0].Protocol = "gemini" },
		func(c *Config) { c.Channels[0].InspectionTemplates = []string{"missing"} },
		func(c *Config) { c.Channels[0].InspectionTemplates = []string{"custom", "custom"} },
		func(c *Config) { c.InspectionTemplates = append(c.InspectionTemplates, c.InspectionTemplates[0]) },
		func(c *Config) { c.Channels[0].PathAllowlist = []AllowedPath{{"GET", "/v1/models", "openai_chat"}} },
	} {
		raw, _ := json.Marshal(c)
		var next Config
		_ = json.Unmarshal(raw, &next)
		mutate(&next)
		if Validate(next) == nil {
			t.Fatal("invalid template configuration accepted", next)
		}
	}
	for _, preset := range InspectionPresets() {
		if _, err := CompileTemplate(preset); err != nil {
			t.Fatal(preset.ID, err)
		}
	}
}

func TestLegacyWhitelistGenerationExclusionConvertsWithoutReenablingInspection(t *testing.T) {
	c := valid()
	c.LegacyRouting = true
	c.Channels[0].PathAllowlist = []AllowedPath{{"POST", "/v1/chat/completions", "openai_chat"}}
	got := migrateInspection(c)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	id := got.Channels[0].InspectionTemplates[0]
	for _, template := range got.InspectionTemplates {
		if template.ID == id {
			compiled, _ := CompileTemplate(template)
			if compiled.Match("POST", "/v1/chat/completions") {
				t.Fatal("previous explicit passthrough became checked")
			}
			return
		}
	}
	t.Fatal("migration template missing")
}

func TestInspectionHTTPStatusSelection(t *testing.T) {
	expression := "301, 400-500，503"
	template := InspectionPresets()[0]
	template.HTTPStatuses = &expression
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{301, 400, 401, 499, 500, 503} {
		if !compiled.ChecksStatus(status) {
			t.Fatal("missing selected status", status)
		}
	}
	for _, status := range []int{200, 300, 302, 399, 501, 502, 504, 599} {
		if compiled.ChecksStatus(status) {
			t.Fatal("unexpected selected status", status)
		}
	}
	// The compiled snapshot owns its ranges even when the input string changes.
	expression = "429"
	if !compiled.ChecksStatus(301) {
		t.Fatal("compiled selection mutated")
	}
	for _, bad := range []string{"100-199", "600", "500-400", "400-", "4xx", "400,,503", "400,", "+400", "400.0", strings.Repeat("400,", 32) + "503"} {
		if _, err := ParseInspectionStatuses(bad); err == nil {
			t.Fatal("invalid status expression accepted", bad)
		}
	}
	for _, valid := range []string{"", "200", " 400 – 499 , 503 ", "400-500,450-503", "599"} {
		if _, err := ParseInspectionStatuses(valid); err != nil {
			t.Fatal(valid, err)
		}
	}
	template.HTTPStatuses = nil
	template.Mode = "json_sse"
	compiled, err = CompileTemplate(template)
	if err != nil || !compiled.ChecksStatus(301) || compiled.ChecksStatus(200) || !compiled.LegacyStatusSelection {
		t.Fatal("initial schema 7 behavior changed")
	}
	empty := ""
	template.HTTPStatuses = &empty
	compiled, err = CompileTemplate(template)
	if err != nil || compiled.ChecksStatus(503) || compiled.LegacyStatusSelection {
		t.Fatal("empty selection must disable HTTP status checks")
	}
	c := valid()
	template.HTTPStatuses = &expression
	c.InspectionTemplates = []InspectionTemplate{template}
	c.Channels[0].InspectionTemplates = []string{template.ID}
	key := bytes.Repeat([]byte{2}, 32)
	data, digest, err := Seal(c, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, digest, key)
	if err != nil || *got.InspectionTemplates[0].HTTPStatuses != "429" {
		t.Fatal("status selection not versioned", err)
	}
}
