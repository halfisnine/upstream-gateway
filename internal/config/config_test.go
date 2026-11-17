package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func valid() Config {
	return Config{Schema: Schema, Channels: []Channel{{ID: "a", Name: "A", BaseURL: "https://example.invalid", Protocols: map[string]Endpoint{"openai_chat": {}}}}}
}
func TestStrictConfig(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":3,"schema_version":3,"channels":[]}`,
		`{"schema_version":3,"channels":[],"unknown":true}`,
		`{"schema_version":3,"channels":[]} {}`,
		`{"schema_version":3,"channels":[{"protocols":{"openai_chat":{},"openai_chat":{}}}]}`,
		`{"schema_version":3,"channels":[{"key":"forbidden"}]}`,
		`{"schema_version":3,"channels":[{"internal_token":"forbidden"}]}`,
		`{"schema_version":3,"channels":[{"protocols":{"gemini":{"key":"forbidden"}}}]}`,
		`{"schema_version":3,"channels":[{"key_set":true}]}`,
	} {
		var c Config
		if Decode([]byte(raw), &c) == nil {
			t.Fatal("accepted invalid configuration or credential field")
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"bad url", func(c *Config) { c.Channels[0].BaseURL = "https://user:pass@example.invalid" }},
		{"http", func(c *Config) { c.Channels[0].BaseURL = "http://localhost" }},
		{"traversal", func(c *Config) { c.Channels[0].BaseURL = "https://example.invalid/a/../b" }},
		{"duplicate channel", func(c *Config) { c.Channels = append(c.Channels, c.Channels[0]) }},
		{"unknown compatibility", func(c *Config) {
			c.Channels[0].Protocols["openai_chat"] = Endpoint{Compatibility: "unverified_sub2api"}
		}},
		{"unknown protocol", func(c *Config) { c.Channels[0].Protocols["custom"] = Endpoint{} }},
		{"legacy schema", func(c *Config) { c.Schema = 2 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := valid()
			test.mutate(&c)
			if Validate(c) == nil {
				t.Fatal("accepted invalid configuration")
			}
			if _, _, e := Seal(c, bytes.Repeat([]byte{7}, 32)); e == nil {
				t.Fatal("sealed invalid configuration")
			}
		})
	}
}
func TestEncryptedSnapshots(t *testing.T) {
	c := valid()
	key := bytes.Repeat([]byte{7}, 32)
	data, digest, e := Seal(c, key)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, []byte(c.Channels[0].BaseURL)) {
		t.Fatal("plaintext config")
	}
	got, e := Open(data, digest, key)
	if e != nil || got.Channels[0].BaseURL != c.Channels[0].BaseURL {
		t.Fatal("snapshot did not round trip", e)
	}
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte(`"key"`)) || bytes.Contains(raw, []byte(`"internal_token"`)) {
		t.Fatal("credential fields in writable schema")
	}
	data[0] ^= 1
	if _, e = Open(data, digest, key); e == nil {
		t.Fatal("accepted tampered snapshot")
	}
}
func TestMultipleProtocolsAndOverride(t *testing.T) {
	c := valid()
	c.Channels[0].Protocols["gemini"] = Endpoint{BaseURL: "https://gemini.example.invalid", Auth: "bearer"}
	e := c.Channels[0].Effective("gemini")
	if e.Auth != "bearer" || e.BaseURL != "https://gemini.example.invalid" {
		t.Fatal(e)
	}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
}
func TestLegacyReadStripsSecretsAndRequiresRepublish(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	nonce := make([]byte, g.NonceSize())
	raw := []byte(`{"schema_version":2,"channels":[{"id":"a","name":"legacy","base_url":"https://example.invalid","key":"OLD_SECRET","internal_token":"OLD_INTERNAL_TOKEN","protocols":{"openai_chat":{},"gemini":{"key":"OLD_OVERRIDE","auth":"bearer"}}}]}`)
	data := g.Seal(nonce, nonce, raw, []byte("upstream-gateway-schema-2"))
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if _, e := Open(data, digest, key); !errors.Is(e, ErrLegacyConfig) {
		t.Fatal("legacy runtime changed auth semantics silently", e)
	}
	converted, source, e := OpenForEdit(data, digest, key)
	if e != nil || source != 2 || converted.Schema != Schema {
		t.Fatal(source, e)
	}
	sanitized, _ := json.Marshal(converted)
	if bytes.Contains(sanitized, []byte("OLD_")) || bytes.Contains(sanitized, []byte(`"key"`)) || bytes.Contains(sanitized, []byte("internal_token")) {
		t.Fatal("legacy secrets escaped converter")
	}
	if converted.Channels[0].Protocols["gemini"].Auth != "bearer" {
		t.Fatal("protocol settings lost")
	}
	newData, newDigest, e := Seal(converted, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Open(newData, newDigest, key); e != nil {
		t.Fatal("converted version failed", e)
	}
}

func TestSchemaThreeRuntimeUpgradeKeepsInspectionOff(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	nonce := make([]byte, g.NonceSize())
	raw := []byte(`{"schema_version":3,"channels":[{"id":"a","name":"A","base_url":"https://example.invalid","protocols":{"openai_chat":{}}}]}`)
	data := g.Seal(nonce, nonce, raw, []byte("upstream-gateway-schema-3"))
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	c, e := Open(data, digest, key)
	if e != nil || c.Schema != Schema || c.InspectEmbeddedErrors || len(c.ErrorRules) != 0 || len(c.Channels) != 1 {
		t.Fatal(c, e)
	}
	edited, source, e := OpenForEdit(data, digest, key)
	if e != nil || source != 3 || edited.Schema != 4 {
		t.Fatal(edited, source, e)
	}
}
func TestRuleValidationRejectsDanglingAndUnsafeConfiguration(t *testing.T) {
	base := valid()
	base.ErrorRules = []Rule{{ID: "test", Name: "test", Enabled: true, Sources: []string{"http"}, Message: "safe"}}
	for _, mutate := range []func(*Rule){func(r *Rule) { r.Channels = []string{"missing"} }, func(r *Rule) { r.ReturnStatus = 200 }, func(r *Rule) { r.Statuses = []StatusRange{{500, 400}} }, func(r *Rule) { r.Sources = []string{"unknown"} }, func(r *Rule) { r.Message = "bad\nmessage" }, func(r *Rule) {
		r.Groups = []ConditionGroup{{Conditions: []Condition{{Field: "raw_body", Operator: "contains", Values: []string{"x"}}}}}
	}} {
		c := base
		c.ErrorRules = append([]Rule(nil), base.ErrorRules...)
		mutate(&c.ErrorRules[0])
		if Validate(c) == nil {
			t.Fatal("invalid rule accepted")
		}
	}
}
