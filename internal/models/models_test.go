package models

import (
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		input        string
		defaultName  string
		wantName     string
		wantMode     int
		wantThink    int
		wantHeader   string
		wantFallback bool
		wantErr      bool
	}{
		{
			input:        "gemini-3.6-flash",
			defaultName:  "gemini-3.6-flash",
			wantName:     "gemini-3.6-flash",
			wantMode:     1,
			wantThink:    4,
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "gemini-3.8-flash",
			defaultName:  "gemini-3.6-flash",
			wantName:     "gemini-3.8-flash",
			wantMode:     1,
			wantThink:    4,
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "gemini-4-argon",
			defaultName:  "gemini-3.6-flash",
			wantName:     "gemini-4-argon",
			wantMode:     8,
			wantThink:    4,
			wantHeader:   "95a4c0abcba24364",
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "argon",
			defaultName:  "gemini-3.6-flash",
			wantName:     "argon",
			wantMode:     8,
			wantThink:    4,
			wantHeader:   "95a4c0abcba24364",
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "gemini-4",
			defaultName:  "gemini-3.6-flash",
			wantName:     "gemini-4",
			wantMode:     8,
			wantThink:    4,
			wantHeader:   "95a4c0abcba24364",
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "argon@think=0",
			defaultName:  "gemini-3.6-flash",
			wantName:     "argon",
			wantMode:     8,
			wantThink:    0,
			wantHeader:   "95a4c0abcba24364",
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "gemini-3.5-flash-thinking",
			defaultName:  "gemini-3.6-flash",
			wantName:     "gemini-3.5-flash-thinking",
			wantMode:     2,
			wantThink:    0,
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "gemini-3.6-flash@think=0",
			defaultName:  "gemini-3.6-flash",
			wantName:     "gemini-3.6-flash",
			wantMode:     1,
			wantThink:    0,
			wantFallback: false,
			wantErr:      false,
		},
		{
			input:        "unknown-model",
			defaultName:  "gemini-3.6-flash",
			wantName:     "gemini-3.6-flash",
			wantMode:     1,
			wantThink:    4,
			wantFallback: true,
			wantErr:      false,
		},
		{
			input:       "gemini-3.6-flash@think=abc",
			defaultName: "gemini-3.6-flash",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			res, err := Resolve(tt.input, tt.defaultName)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Resolve(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if res.Name != tt.wantName {
				t.Errorf("res.Name = %q, want %q", res.Name, tt.wantName)
			}
			if res.Mode != tt.wantMode {
				t.Errorf("res.Mode = %d, want %d", res.Mode, tt.wantMode)
			}
			if res.Think != tt.wantThink {
				t.Errorf("res.Think = %d, want %d", res.Think, tt.wantThink)
			}
			if res.Header != tt.wantHeader {
				t.Errorf("res.Header = %q, want %q", res.Header, tt.wantHeader)
			}
			if res.Fallback != tt.wantFallback {
				t.Errorf("res.Fallback = %v, want %v", res.Fallback, tt.wantFallback)
			}
		})
	}
}

func TestResolveEnhanced(t *testing.T) {
	res, err := Resolve("gemini-3.1-pro-enhanced", "gemini-3.6-flash")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if res.Extra == nil || res.Extra[31] != 2 || res.Extra[80] != 3 {
		t.Errorf("Expected extra fields {31:2, 80:3}, got %v", res.Extra)
	}
}

func TestResolveDynamic(t *testing.T) {
	dynamic := []DynamicModel{
		{Name: "gemini-4-carbon", Ticket: "carbon-ticket", Desc: "4 Carbon", Mode: 8},
		{Name: "gemini-4-neon", Ticket: "neon-ticket", Desc: "4 Neon", Mode: 8},
	}

	t.Run("dynamic exact name", func(t *testing.T) {
		res, err := Resolve("gemini-4-carbon", "gemini-3.6-flash", dynamic...)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if res.Name != "gemini-4-carbon" || res.Mode != 8 || res.Header != "carbon-ticket" {
			t.Errorf("Unexpected resolved model: %+v", res)
		}
		if res.Fallback {
			t.Error("Expected dynamic model not to be marked as fallback")
		}
	})

	t.Run("dynamic alias without prefix", func(t *testing.T) {
		res, err := Resolve("4-neon", "gemini-3.6-flash", dynamic...)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if res.Name != "gemini-4-neon" || res.Header != "neon-ticket" {
			t.Errorf("Unexpected resolved model: %+v", res)
		}
	})

	t.Run("dynamic think override", func(t *testing.T) {
		res, err := Resolve("gemini-4-carbon@think=2", "gemini-3.6-flash", dynamic...)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if res.Think != 2 {
			t.Errorf("res.Think = %d, want 2", res.Think)
		}
	})

	t.Run("static takes precedence over dynamic", func(t *testing.T) {
		override := []DynamicModel{
			{Name: "gemini-4-hydrogen", Ticket: "different-ticket", Mode: 99},
		}
		res, err := Resolve("gemini-4-hydrogen", "gemini-3.6-flash", override...)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if res.Header != "56fdd199312815e2" || res.Mode != 1 {
			t.Errorf("Expected static Hydrogen entry, got %+v", res)
		}
	})

	t.Run("unknown falls back", func(t *testing.T) {
		res, err := Resolve("gemini-9.9-fake", "gemini-3.6-flash", dynamic...)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !res.Fallback || res.Name != "gemini-3.6-flash" {
			t.Errorf("Unexpected fallback result: %+v", res)
		}
	})
}

func TestDynamicNameFromDisplay(t *testing.T) {
	tests := []struct {
		display string
		want    string
		ok      bool
	}{
		{display: "4 Hydrogen", want: "gemini-4-hydrogen", ok: true},
		{display: "4 Carbon", want: "gemini-4-carbon", ok: true},
		{display: "3.8 Flash", want: "gemini-3.8-flash", ok: true},
		{display: "3.5 Flash-Lite", want: "gemini-3.5-flash-lite", ok: true},
		{display: " 4  Neon ", want: "gemini-4-neon", ok: true},
		{display: "", ok: false},
		{display: "Weird", ok: false},
		{display: "X 4", ok: false},
	}

	for _, tt := range tests {
		got, ok := DynamicNameFromDisplay(tt.display)
		if ok != tt.ok || got != tt.want {
			t.Errorf("DynamicNameFromDisplay(%q) = (%q, %v), want (%q, %v)", tt.display, got, ok, tt.want, tt.ok)
		}
	}
}
