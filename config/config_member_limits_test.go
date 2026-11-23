package config

import (
	"strings"
	"testing"
)

// TestLoadRejectsInvalidMemberLimits：會員限流的兩個設定不是合法整數時拒絕啟動。
func TestLoadRejectsInvalidMemberLimits(t *testing.T) {
	setRequired(t)
	t.Setenv("MCP_MEMBER_REQUESTS_PER_MINUTE", "many")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_MEMBER_REQUESTS_PER_MINUTE") {
		t.Fatalf("每分鐘次數不是整數應回錯誤：%v", err)
	}
	t.Setenv("MCP_MEMBER_REQUESTS_PER_MINUTE", "30")
	t.Setenv("MCP_MEMBER_REQUEST_BURST", "lots")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_MEMBER_REQUEST_BURST") {
		t.Fatalf("突發上限不是整數應回錯誤：%v", err)
	}
}
