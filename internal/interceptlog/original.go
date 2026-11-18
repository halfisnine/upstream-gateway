package interceptlog

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxOriginalBytes = 2048

// Samples have a separate key domain and are bound to the immutable event ID.
func originalCipher(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("invalid_original_key")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("upstream-gateway-original-error-v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

var sensitiveJSON = regexp.MustCompile(`(?i)("(?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|authorization|x-api-key|x-goog-api-key|password|secret)"\s*:\s*)"(?:\\.|[^"\\])*"?`)
var bearer = regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=\-]+`)

// CaptureOriginal retains only a small UTF-8 excerpt. It never changes matching
// or the downstream response. Current request credentials are removed before
// truncation, including common JSON and URL encodings.
func (e *Event) CaptureOriginal(key []byte, raw []byte, credential string, partial bool) {
	e.OriginalCipher = nil
	e.OriginalTruncated = false
	e.OriginalRedacted = false
	e.OriginalState = "unavailable"
	if raw == nil {
		return
	}
	e.OriginalTruncated = partial || len(raw) > MaxOriginalBytes
	// Look ahead beyond the retained prefix to redact credentials crossing its edge.
	window := min(len(raw), MaxOriginalBytes+max(1024, len(credential)*6))
	text := strings.ToValidUTF8(string(raw[:window]), "�")
	before := text
	if credential != "" {
		encoded, _ := json.Marshal(credential)
		forms := []string{credential, string(encoded[1 : len(encoded)-1]), url.QueryEscape(credential), url.PathEscape(credential)}
		for _, secret := range forms {
			if secret != "" {
				text = strings.ReplaceAll(text, secret, "[REDACTED]")
			}
		}
	}
	text = sensitiveJSON.ReplaceAllString(text, `${1}"[REDACTED]"`)
	text = bearer.ReplaceAllString(text, "Bearer [REDACTED]")
	e.OriginalRedacted = text != before
	if len(text) > MaxOriginalBytes {
		text = text[:MaxOriginalBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		e.OriginalTruncated = true
	}
	aead, err := originalCipher(key)
	if err != nil {
		e.OriginalState = "encryption_failed"
		return
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		e.OriginalState = "encryption_failed"
		return
	}
	e.OriginalCipher = aead.Seal(nonce, nonce, []byte(text), []byte(e.ID))
	e.OriginalState = "captured"
}
func (e Event) OpenOriginal(key []byte) (string, error) {
	if e.OriginalState != "captured" {
		return "", nil
	}
	aead, err := originalCipher(key)
	if err != nil {
		return "", err
	}
	if len(e.OriginalCipher) < aead.NonceSize() || len(e.OriginalCipher) > MaxOriginalBytes+aead.NonceSize()+aead.Overhead() {
		return "", errors.New("invalid_original_cipher")
	}
	raw, err := aead.Open(nil, e.OriginalCipher[:aead.NonceSize()], e.OriginalCipher[aead.NonceSize():], []byte(e.ID))
	if err != nil {
		return "", errors.New("original_decryption_failed")
	}
	return string(raw), nil
}
