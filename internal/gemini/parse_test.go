package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractResponseWithMeta(t *testing.T) {
	innerJSON, _ := json.Marshal([]any{
		nil,
		[]any{"c_123", "r_456"},
		nil, nil,
		[]any{
			[]any{"rc_789", []any{"Hello meta"}},
		},
		nil, nil, nil, nil, nil,
		"padding_for_minimum_inner_length_threshold_check_50_chars",
	})
	outerJSON, _ := json.Marshal([]any{
		[]any{"wrb.fr", nil, string(innerJSON), nil, nil, nil, "generic"},
	})
	raw := ")]}'\n\n" + string(outerJSON) + strings.Repeat(" ", 200) + "\n"

	text, meta, err := ExtractResponseWithMeta(raw)
	if err != nil {
		t.Fatalf("ExtractResponseWithMeta error: %v", err)
	}
	if text != "Hello meta" {
		t.Errorf("text = %q, want %q", text, "Hello meta")
	}
	if meta.CID != "c_123" || meta.RID != "r_456" || meta.RCID != "rc_789" {
		t.Errorf("unexpected meta: %+v", meta)
	}
}

func TestCleanText(t *testing.T) {
	input := "Hello world\n```python?code_reference&code_event_index=1\nprint('hi')```\nhttp://googleusercontent.com/card_content/123\nExtra text"
	cleaned := CleanText(input, true)
	expected := "Hello world\nExtra text"
	if cleaned != expected {
		t.Errorf("CleanText got %q, want %q", cleaned, expected)
	}
}

func TestIsBardError(t *testing.T) {
	raw := "something BardErrorInfo [1024] happened"
	code, ok := IsBardError(raw)
	if !ok || code != "1024" {
		t.Errorf("IsBardError got (%q, %t), want (\"1024\", true)", code, ok)
	}
}
