package apikey

import (
	"strings"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	seen := make(map[string]struct{})
	for range 256 {
		key, err := generateKey()
		if err != nil {
			t.Fatalf("generateKey(): %v", err)
		}
		if !strings.HasPrefix(key.Full, "mcp_live_") {
			t.Fatalf("格式錯誤:%q", key.Full)
		}
		if prefix, err := parsePrefix(key.Full); err != nil || prefix != key.Prefix {
			t.Fatalf("無法解析產生的 key: prefix=%q err=%v", prefix, err)
		}
		if _, exists := seen[key.Full]; exists {
			t.Fatal("產生重複 API key")
		}
		seen[key.Full] = struct{}{}
	}
}

func TestParsePrefixRejectsMalformedKeys(t *testing.T) {
	valid, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		"",
		"mcp_live_",
		"mcp_live_a_b",
		valid.Full + "_extra",
		strings.Replace(valid.Full, "mcp_live_", "mcp_test_", 1),
		valid.Full[:len(valid.Full)-1] + "!",
	} {
		if _, err := parsePrefix(candidate); err == nil {
			t.Errorf("畸形 key 應被拒絕:%q", candidate)
		}
	}
}

func TestHMACVerification(t *testing.T) {
	pepper := []byte("01234567890123456789012345678901")
	key, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := keyDigest(pepper, key.Full)
	if !secureDigestEqual(keyDigest(pepper, key.Full), want) {
		t.Fatal("正確 API key 應通過")
	}
	if secureDigestEqual(keyDigest(pepper, key.Full+"x"), want) {
		t.Fatal("被修改的 API key 不應通過")
	}
	if secureDigestEqual(keyDigest([]byte("different-pepper-32-bytes-value!!"), key.Full), want) {
		t.Fatal("錯誤 pepper 不應通過")
	}
}
