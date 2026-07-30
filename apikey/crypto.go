package apikey

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	keyScheme       = "mcp_live_"
	legacyScheme    = "mcp_legacy_"
	publicIDBytes   = 12
	secretBytes     = 32
	randomIDBytes   = 16
	minPepperLength = 32
)

var errMalformedKey = errors.New("API key 格式不正確")

type generatedKey struct {
	ID     string
	Prefix string
	Full   string
	Masked string
}

func generateKey() (generatedKey, error) {
	publicID, err := randomURLSafe(publicIDBytes)
	if err != nil {
		return generatedKey{}, fmt.Errorf("產生 API key public ID: %w", err)
	}
	secret, err := randomURLSafe(secretBytes)
	if err != nil {
		return generatedKey{}, fmt.Errorf("產生 API key secret: %w", err)
	}
	id, err := randomURLSafe(randomIDBytes)
	if err != nil {
		return generatedKey{}, fmt.Errorf("產生 API key ID: %w", err)
	}
	prefix := keyScheme + publicID
	return generatedKey{
		ID:     id,
		Prefix: prefix,
		Full:   prefix + "_" + secret,
		Masked: maskPrefix(prefix),
	}, nil
}

func randomURLSafe(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validatePepper(pepper []byte) error {
	if len(pepper) < minPepperLength {
		return fmt.Errorf("MCP_API_KEY_PEPPER 至少需要 %d bytes", minPepperLength)
	}
	return nil
}

func keyDigest(pepper []byte, fullKey string) []byte {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(fullKey))
	return mac.Sum(nil)
}

func secureDigestEqual(got, want []byte) bool {
	return len(got) == sha256.Size && len(want) == sha256.Size &&
		subtle.ConstantTimeCompare(got, want) == 1
}

func parsePrefix(fullKey string) (string, error) {
	if !strings.HasPrefix(fullKey, keyScheme) {
		return "", errMalformedKey
	}
	rest := strings.TrimPrefix(fullKey, keyScheme)
	publicIDLength := base64.RawURLEncoding.EncodedLen(publicIDBytes)
	if len(rest) <= publicIDLength || rest[publicIDLength] != '_' {
		return "", errMalformedKey
	}
	publicID, secret := rest[:publicIDLength], rest[publicIDLength+1:]
	publicRaw, err := base64.RawURLEncoding.DecodeString(publicID)
	if err != nil || len(publicRaw) != publicIDBytes {
		return "", errMalformedKey
	}
	secretRaw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(secretRaw) != secretBytes {
		return "", errMalformedKey
	}
	return keyScheme + publicID, nil
}

func legacyPrefix(pepper []byte, fullKey string) string {
	sum := keyDigest(pepper, fullKey)
	return legacyScheme + hex.EncodeToString(sum[:8])
}

func lookupPrefix(pepper []byte, fullKey string) string {
	if prefix, err := parsePrefix(fullKey); err == nil {
		return prefix
	}
	return legacyPrefix(pepper, fullKey)
}

func maskPrefix(prefix string) string {
	return prefix + "_••••••••••••"
}
