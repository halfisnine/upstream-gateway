package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

const Schema = 3

var ErrLegacyConfig = errors.New("legacy_configuration_requires_republish")

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var Protocols = []string{"openai_chat", "openai_responses", "anthropic_messages", "gemini"}

type Endpoint struct {
	BaseURL       string `json:"base_url,omitempty"`
	Auth          string `json:"auth,omitempty"`
	Compatibility string `json:"compatibility,omitempty"`
}
type Channel struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	BaseURL   string              `json:"base_url"`
	AllowHTTP bool                `json:"allow_http,omitempty"`
	Protocols map[string]Endpoint `json:"protocols"`
}
type Config struct {
	Schema   int       `json:"schema_version"`
	Channels []Channel `json:"channels"`
}

func Empty() Config { return Config{Schema: Schema, Channels: []Channel{}} }

// Decode rejects both duplicate keys and unknown fields, including in nested objects.
func Decode(data []byte, dst any) error {
	if len(data) > 2<<20 {
		return errors.New("configuration_too_large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate_json_key")
				}
				seen[s] = true
				if e = walk(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := walk(); e != nil {
					return e
				}
			}
		default:
			return errors.New("invalid_json")
		}
		_, e = d.Token()
		return e
	}
	if err := walk(); err != nil {
		return errors.New("invalid_or_duplicate_json")
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing_json")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return errors.New("unknown_field_or_invalid_json")
	}
	return nil
}
func DefaultAuth(p string) string {
	switch p {
	case "anthropic_messages":
		return "x_api_key"
	case "gemini":
		return "x_goog_api_key"
	default:
		return "bearer"
	}
}
func (c Channel) Effective(p string) Endpoint {
	e := c.Protocols[p]
	if e.BaseURL == "" {
		e.BaseURL = c.BaseURL
	}
	if e.Auth == "" {
		e.Auth = DefaultAuth(p)
	}
	if e.Compatibility == "" {
		e.Compatibility = "standard_v1"
	}
	return e
}
func IsProtocol(p string) bool {
	for _, v := range Protocols {
		if p == v {
			return true
		}
	}
	return false
}
func ValidID(id string) bool { return idPattern.MatchString(id) }
func validURL(raw string, allowHTTP bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return false
	}
	if strings.ContainsAny(raw, "\\\r\n\t ") || strings.Contains(u.EscapedPath(), "%") || strings.Contains(u.Path, "//") {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
func Validate(c Config) error {
	if c.Schema != Schema {
		return errors.New("unsupported_schema")
	}
	if len(c.Channels) > 128 {
		return errors.New("too_many_channels")
	}
	ids := map[string]bool{}
	for i, ch := range c.Channels {
		field := fmt.Sprintf("channels[%d]", i)
		fail := func(s string) error { return errors.New(field + "." + s) }
		if !ValidID(ch.ID) || ids[ch.ID] {
			return fail("invalid_or_duplicate_id")
		}
		ids[ch.ID] = true
		if len(ch.Name) > 100 || strings.TrimSpace(ch.Name) == "" {
			return fail("invalid_name")
		}
		if !validURL(ch.BaseURL, ch.AllowHTTP) {
			return fail("invalid_base_url")
		}
		if len(ch.Protocols) == 0 || len(ch.Protocols) > 4 {
			return fail("invalid_protocols")
		}
		for p, e := range ch.Protocols {
			if !IsProtocol(p) {
				return fail("unsupported_protocol")
			}
			if e.Compatibility != "" && e.Compatibility != "standard_v1" {
				return fail("unsupported_compatibility")
			}
			eff := ch.Effective(p)
			if !validURL(eff.BaseURL, ch.AllowHTTP) {
				return fail(p + ".invalid_base_url")
			}
			if eff.Auth != "bearer" && eff.Auth != "x_api_key" && eff.Auth != "x_goog_api_key" {
				return fail(p + ".unsupported_auth")
			}
		}
	}
	return nil
}

func Seal(c Config, key []byte) ([]byte, string, error) {
	if err := Validate(c); err != nil {
		return nil, "", err
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return nil, "", e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return nil, "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, "", e
	}
	data := g.Seal(nonce, nonce, raw, []byte("upstream-gateway-schema-3"))
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// Open is used by runtime nodes. A legacy snapshot must be explicitly republished.
func Open(data []byte, digest string, key []byte) (Config, error) {
	c, source, err := OpenForEdit(data, digest, key)
	if err != nil {
		return c, err
	}
	if source != Schema {
		return Empty(), ErrLegacyConfig
	}
	return c, nil
}

// OpenForEdit never returns stored legacy credentials. The returned config is
// always the current schema, while source identifies the immutable original.
func OpenForEdit(data []byte, digest string, key []byte) (Config, int, error) {
	fail := func(e string) (Config, int, error) { return Empty(), 0, errors.New(e) }
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return fail("snapshot_digest_mismatch")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fail("invalid_master_key")
	}
	g, err := cipher.NewGCM(block)
	if err != nil || len(data) < g.NonceSize() {
		return fail("invalid_snapshot")
	}
	for _, schema := range []int{Schema, 2} {
		raw, err := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(fmt.Sprintf("upstream-gateway-schema-%d", schema)))
		if err != nil {
			continue
		}
		if schema == Schema {
			c := Empty()
			if err = Decode(raw, &c); err != nil {
				return Empty(), 0, err
			}
			return c, schema, Validate(c)
		}
		return convertLegacy(raw)
	}
	return fail("snapshot_decryption_failed")
}

// These types are deliberately separate from the writable schema. Legacy
// fields are accepted only while decrypting historical, immutable snapshots.
func convertLegacy(raw []byte) (Config, int, error) {
	type legacyEndpoint struct {
		BaseURL       string `json:"base_url,omitempty"`
		Key           string `json:"key,omitempty"`
		KeySet        bool   `json:"key_set,omitempty"`
		Auth          string `json:"auth,omitempty"`
		Compatibility string `json:"compatibility,omitempty"`
	}
	var old struct {
		Schema   int `json:"schema_version"`
		Channels []struct {
			ID        string                    `json:"id"`
			Name      string                    `json:"name"`
			BaseURL   string                    `json:"base_url"`
			Key       string                    `json:"key,omitempty"`
			KeySet    bool                      `json:"key_set,omitempty"`
			Token     string                    `json:"internal_token,omitempty"`
			TokenSet  bool                      `json:"token_set,omitempty"`
			AllowHTTP bool                      `json:"allow_http,omitempty"`
			Protocols map[string]legacyEndpoint `json:"protocols"`
		} `json:"channels"`
	}
	if err := Decode(raw, &old); err != nil {
		return Empty(), 0, err
	}
	if old.Schema != 2 {
		return Empty(), 0, errors.New("unsupported_schema")
	}
	c := Empty()
	for _, ch := range old.Channels {
		next := Channel{ID: ch.ID, Name: ch.Name, BaseURL: ch.BaseURL, AllowHTTP: ch.AllowHTTP, Protocols: map[string]Endpoint{}}
		for p, e := range ch.Protocols {
			next.Protocols[p] = Endpoint{BaseURL: e.BaseURL, Auth: e.Auth, Compatibility: e.Compatibility}
		}
		c.Channels = append(c.Channels, next)
	}
	return c, 2, Validate(c)
}
