package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestAllowedPathsValidationAndSnapshots(t *testing.T) {
	base := AllowedPath{Method: "GET", Path: "/v1/models", Protocol: "openai_chat"}
	c := valid()
	c.Channels[0].PathAllowlist = []AllowedPath{base}
	key := bytes.Repeat([]byte{7}, 32)
	data, digest, err := Seal(c, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, digest, key)
	if err != nil || got.Channels[0].PathAllowlist[0] != base {
		t.Fatal("allowlist lost", err)
	}
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
