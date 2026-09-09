package gemini

import (
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestApplyPersistenceFlagsTemporary(t *testing.T) {
	cfg := config.Config{TemporaryChats: true}
	inner := make([]any, GeminiPayloadSize)
	applyPersistenceFlags(inner, cfg)

	if inner[41] == nil {
		t.Fatal("inner[41] should not be nil")
	}
	flag, ok := inner[41].([]any)
	if !ok {
		t.Fatalf("inner[41] should be []any, got %T", inner[41])
	}
	if len(flag) != 1 || flag[0] != 1 {
		t.Errorf("inner[41] = %v, want [1]", flag)
	}
	if inner[45] != 1 {
		t.Errorf("inner[45] = %v, want 1", inner[45])
	}
}

func TestApplyPersistenceFlagsPersistent(t *testing.T) {
	cfg := config.Config{TemporaryChats: false}
	inner := make([]any, GeminiPayloadSize)
	applyPersistenceFlags(inner, cfg)

	flag, ok := inner[41].([]any)
	if !ok {
		t.Fatalf("inner[41] should be []any, got %T", inner[41])
	}
	if len(flag) != 1 || flag[0] != 2 {
		t.Errorf("inner[41] = %v, want [2]", flag)
	}
}

func TestBuildURLWithBLOverride(t *testing.T) {
	cfg := config.Config{
		AuthUser: "",
		GeminiBL: "old-bl",
	}
	url1 := BuildURL(cfg, "")
	if !strings.Contains(url1, "bl=old-bl") {
		t.Errorf("BuildURL with empty override should use cfg.GeminiBL, got %s", url1)
	}

	url2 := BuildURL(cfg, "new-bl")
	if !strings.Contains(url2, "bl=new-bl") {
		t.Errorf("BuildURL with override should use override, got %s", url2)
	}
}
