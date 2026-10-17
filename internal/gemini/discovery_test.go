package gemini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestComputeCapacity(t *testing.T) {
	tests := []struct {
		name      string
		tier      []any
		capab     []any
		wantCap   int
		wantField int
	}{
		{
			name:      "Tier 21 override field 13",
			tier:      []any{float64(21)},
			capab:     []any{},
			wantCap:   1,
			wantField: 13,
		},
		{
			name:      "Plus accounts (capab 115)",
			tier:      []any{float64(8)},
			capab:     []any{float64(115)},
			wantCap:   4,
			wantField: 12,
		},
		{
			name:      "Pro accounts (tier 16 or capab 106)",
			tier:      []any{float64(16)},
			capab:     []any{},
			wantCap:   3,
			wantField: 12,
		},
		{
			name:      "Pro accounts (tier 8 or capab 19)",
			tier:      []any{float64(8), float64(0)},
			capab:     []any{float64(2), float64(140)},
			wantCap:   2,
			wantField: 12,
		},
		{
			name:      "Free accounts fallback",
			tier:      []any{float64(1)},
			capab:     []any{float64(2)},
			wantCap:   1,
			wantField: 12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCap, gotField := computeCapacity(tt.tier, tt.capab)
			if gotCap != tt.wantCap || gotField != tt.wantField {
				t.Errorf("computeCapacity() = (%d, %d), want (%d, %d)", gotCap, gotField, tt.wantCap, tt.wantField)
			}
		})
	}
}

func TestResolveModelHeader(t *testing.T) {
	tmpDir := t.TempDir()
	cookiePath := filepath.Join(tmpDir, "gemini-auth.json")
	if err := os.WriteFile(cookiePath, []byte(`{"cookie":"dummy_cookie=xyz; SAPISID=abc123"}`), 0600); err != nil {
		t.Fatalf("failed to create temp cookie file: %v", err)
	}

	client := NewClient(config.Default())
	client.Cookies = NewCookieCache(cookiePath)
	client.accountCap = 2
	client.accountCapFld = 12
	client.discovered = []DiscoveredModel{
		{
			ID:          "95a4c0abcba24364",
			Category:    "Argon",
			Display:     "4 Argon",
			Desc:        "Complex tasks",
			Number:      8,
			Capacity:    2,
			CapacityFld: 12,
		},
		{
			ID:          "56fdd199312815e2",
			Category:    "Flash",
			Display:     "3.8 Flash",
			Desc:        "All-around help",
			Number:      1,
			Capacity:    2,
			CapacityFld: 12,
		},
	}

	reqUUID := "TEST-UUID-1234"

	// Case 1: Authenticated session matching discovered model (Argon)
	optArgon := ModelOptions{
		ModelName:   "gemini-4-argon",
		ModelID:     8,
		ThinkMode:   4,
		ModelHeader: "95a4c0abcba24364",
	}
	hdr, effMode := client.resolveModelHeader(optArgon, reqUUID)
	if effMode != 8 {
		t.Errorf("effMode = %d, want 8", effMode)
	}
	if !strings.Contains(hdr, `"95a4c0abcba24364"`) {
		t.Errorf("expected header to contain model ID, got %s", hdr)
	}
	if !strings.Contains(hdr, `TEST-UUID-1234`) {
		t.Errorf("expected header to contain reqUUID, got %s", hdr)
	}

	// Case 2: Static fallback when discovered list is empty but ModelHeader is configured
	client.discovered = nil
	hdrStatic, effModeStatic := client.resolveModelHeader(optArgon, reqUUID)
	if effModeStatic != 8 {
		t.Errorf("effModeStatic = %d, want 8", effModeStatic)
	}
	if !strings.Contains(hdrStatic, `"95a4c0abcba24364"`) {
		t.Errorf("expected fallback header to contain model ID, got %s", hdrStatic)
	}

	// Case 3: Anonymous session (no cookie) must NOT attach model header even for Argon
	anonClient := NewClient(config.Default())
	anonClient.Cookies = nil
	hdrAnon, effModeAnon := anonClient.resolveModelHeader(optArgon, reqUUID)
	if hdrAnon != "" {
		t.Errorf("expected empty header for anonymous session, got %s", hdrAnon)
	}
	if effModeAnon != 8 {
		t.Errorf("effModeAnon = %d, want 8", effModeAnon)
	}

	// Case 4: Standard / legacy model without ModelHeader (e.g. gemini-3.5-flash-thinking)
	// Must NOT attach model header and must preserve its own ModelID
	optThinking := ModelOptions{
		ModelName:   "gemini-3.5-flash-thinking",
		ModelID:     1,
		ThinkMode:   2,
		ModelHeader: "",
		Extra:       map[int]any{80: 2},
	}
	hdrThinking, effModeThinking := client.resolveModelHeader(optThinking, reqUUID)
	if hdrThinking != "" {
		t.Errorf("expected empty header for model without ModelHeader, got %s", hdrThinking)
	}
	if effModeThinking != 1 {
		t.Errorf("effModeThinking = %d, want 1", effModeThinking)
	}

	// Case 5: Thinking tail logic
	optThinkingEnabled := ModelOptions{
		ModelName:   "gemini-4-argon",
		ModelID:     8,
		ModelHeader: "95a4c0abcba24364",
		Extra:       map[int]any{80: 2},
	}
	hdrThinkingEnabled, _ := client.resolveModelHeader(optThinkingEnabled, reqUUID)
	if !strings.Contains(hdrThinkingEnabled, ",2,\"TEST-UUID-1234\"]") {
		t.Errorf("expected thinking tail 2 in header, got %s", hdrThinkingEnabled)
	}
}
