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

const Schema = 7

var ErrLegacyConfig = errors.New("legacy_configuration_requires_republish")

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var Protocols = []string{"openai_chat", "openai_responses", "anthropic_messages", "gemini"}

type Endpoint struct {
	BaseURL       string `json:"base_url,omitempty"`
	Auth          string `json:"auth,omitempty"`
	Compatibility string `json:"compatibility,omitempty"`
}
type Channel struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	BaseURL             string              `json:"base_url"`
	AllowHTTP           bool                `json:"allow_http,omitempty"`
	Protocols           map[string]Endpoint `json:"protocols"`
	PathAllowlist       []AllowedPath       `json:"path_allowlist,omitempty"` // Legacy snapshots only.
	InspectionTemplates []string            `json:"inspection_templates,omitempty"`
}

// AllowedPath selects an existing protocol's upstream and credential settings.
// Method and path are exact matches; query strings are not part of the match.
type AllowedPath struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Protocol string `json:"protocol"`
}

var literalPath = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()+,;=:@/-]*$`)

func ValidAllowedPath(path string) bool {
	if len(path) > 1024 || !literalPath.MatchString(path) || strings.Contains(path, "//") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func ValidAllowedMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	default:
		return false
	}
}

// TrafficLimits apply independently on each node. Zero means the safe default,
// never unlimited. Upstream concurrency counts requests, including HTTP/2 streams.
type TrafficLimits struct {
	Node     int `json:"node_concurrency,omitempty"`
	Upstream int `json:"upstream_concurrency,omitempty"`
}

func (l TrafficLimits) Effective() TrafficLimits {
	if l.Node == 0 {
		l.Node = 32
	}
	if l.Upstream == 0 {
		l.Upstream = 16
	}
	return l
}

type Config struct {
	Schema                int                  `json:"schema_version"`
	LegacyRouting         bool                 `json:"-"`
	InspectionTemplates   []InspectionTemplate `json:"inspection_templates,omitempty"`
	TrafficLimits         TrafficLimits        `json:"traffic_limits"`
	Channels              []Channel            `json:"channels"`
	InspectEmbeddedErrors bool                 `json:"inspect_embedded_errors"`
	ErrorRules            []Rule               `json:"error_rules,omitempty"`
}

func Empty() Config {
	return Config{Schema: Schema, Channels: []Channel{}, InspectionTemplates: InspectionPresets()}
}

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
	limits := c.TrafficLimits.Effective()
	if limits.Node < 1 || limits.Node > 8192 || limits.Upstream < 1 || limits.Upstream > limits.Node {
		return errors.New("invalid_traffic_limits")
	}
	if len(c.Channels) > 128 {
		return errors.New("too_many_channels")
	}
	templates := map[string]InspectionTemplate{}
	if !c.LegacyRouting {
		if len(c.InspectionTemplates) > 1024 {
			return errors.New("too_many_inspection_templates")
		}
		for _, t := range c.InspectionTemplates {
			if _, exists := templates[t.ID]; exists {
				return errors.New("duplicate_inspection_template")
			}
			if _, err := CompileTemplate(t); err != nil {
				return err
			}
			templates[t.ID] = t
		}
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
		if (c.LegacyRouting && len(ch.Protocols) == 0) || len(ch.Protocols) > 4 {
			return fail("invalid_protocols")
		}
		if !c.LegacyRouting {
			if len(ch.PathAllowlist) > 0 {
				return fail("legacy_allowlist_requires_conversion")
			}
			if len(ch.InspectionTemplates) > 64 {
				return fail("too_many_inspection_templates")
			}
			seen := map[string]bool{}
			for _, id := range ch.InspectionTemplates {
				t, ok := templates[id]
				if !ok || seen[id] {
					return fail("missing_or_duplicate_inspection_template")
				}
				if _, ok := ch.Protocols[t.Protocol]; !ok {
					return fail("inspection_protocol_not_enabled")
				}
				seen[id] = true
			}
		}
		if len(ch.PathAllowlist) > 64 {
			return fail("too_many_allowed_paths")
		}
		paths := map[string]bool{}
		for _, route := range ch.PathAllowlist {
			key := route.Method + " " + route.Path
			if !ValidAllowedMethod(route.Method) || !ValidAllowedPath(route.Path) || paths[key] {
				return fail("invalid_or_duplicate_allowed_path")
			}
			if _, ok := ch.Protocols[route.Protocol]; !ok {
				return fail("allowed_path_protocol_not_enabled")
			}
			paths[key] = true
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
	return ValidateRules(c.ErrorRules, c.Channels)
}

func Seal(c Config, key []byte) ([]byte, string, error) {
	if c.LegacyRouting {
		return nil, "", errors.New("legacy_configuration_requires_conversion")
	}
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
	data := g.Seal(nonce, nonce, raw, []byte(fmt.Sprintf("upstream-gateway-schema-%d", Schema)))
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// Open is used by runtime nodes. A legacy snapshot must be explicitly republished.
func Open(data []byte, digest string, key []byte) (Config, error) {
	c, source, err := openStored(data, digest, key)
	if err != nil {
		return c, err
	}
	if source != Schema && source != 6 && source != 5 && source != 4 && source != 3 {
		return Empty(), ErrLegacyConfig
	}
	return c, nil
}

// OpenForEdit never returns stored legacy credentials. The returned config is
// always the current schema, while source identifies the immutable original.
func OpenForEdit(data []byte, digest string, key []byte) (Config, int, error) {
	c, source, err := openStored(data, digest, key)
	if err != nil {
		return c, source, err
	}
	if source < Schema {
		c = migrateInspection(c)
	}
	return c, source, Validate(c)
}

func openStored(data []byte, digest string, key []byte) (Config, int, error) {
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
	for _, schema := range []int{Schema, 6, 5, 4, 3, 2} {
		raw, err := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(fmt.Sprintf("upstream-gateway-schema-%d", schema)))
		if err != nil {
			continue
		}
		if schema == Schema || schema == 6 || schema == 5 || schema == 4 || schema == 3 {
			var c Config
			if err = Decode(raw, &c); err != nil {
				return Empty(), 0, err
			}
			if c.Schema != schema {
				return fail("unsupported_schema")
			}
			if schema < 5 {
				c.TrafficLimits = TrafficLimits{}
			}
			if schema < 6 {
				for i := range c.Channels {
					c.Channels[i].PathAllowlist = nil
				}
			}
			c.Schema = Schema
			c.LegacyRouting = schema < Schema
			if schema == 3 {
				c.InspectEmbeddedErrors = false
				c.ErrorRules = nil
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
