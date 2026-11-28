package gemini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCookieCacheLoadBrowserJSONArray(t *testing.T) {
	tmpDir := t.TempDir()
	cookiePath := filepath.Join(tmpDir, "cookie.json")
	payload := `[
		{"domain": ".google.com", "name": "SAPISID", "value": "sap_val_1"},
		{"domain": ".gemini.google.com", "name": "_ga", "value": "GA1.1"},
		{"domain": "gemini.google.com", "name": "OSID", "value": "osid_val"},
		{"domain": ".example.com", "name": "OTHER", "value": "ignored"}
	]`
	if err := os.WriteFile(cookiePath, []byte(payload), 0600); err != nil {
		t.Fatalf("write temp cookie file: %v", err)
	}

	cache := NewCookieCache(cookiePath)
	info, err := cache.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if info.SAPISID != "sap_val_1" {
		t.Errorf("SAPISID = %q, want %q", info.SAPISID, "sap_val_1")
	}

	wantCookie := "SAPISID=sap_val_1; _ga=GA1.1; OSID=osid_val"
	if info.Cookie != wantCookie {
		t.Errorf("Cookie = %q, want %q", info.Cookie, wantCookie)
	}

	if strings.Contains(info.Cookie, "OTHER=") {
		t.Errorf("non-Google cookie should have been filtered out: %q", info.Cookie)
	}
}

func TestCookieCacheLoadObjectFormat(t *testing.T) {
	tmpDir := t.TempDir()
	cookiePath := filepath.Join(tmpDir, "gemini-auth.json")
	payload := `{
		"cookie": "SID=abc; SAPISID=sap_obj",
		"auth_user": 1,
		"xsrf_token": "xsrf123",
		"gemini_bl": "bl_val"
	}`
	if err := os.WriteFile(cookiePath, []byte(payload), 0600); err != nil {
		t.Fatalf("write temp cookie file: %v", err)
	}

	cache := NewCookieCache(cookiePath)
	info, err := cache.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if info.SAPISID != "sap_obj" {
		t.Errorf("SAPISID = %q, want %q", info.SAPISID, "sap_obj")
	}
	if info.AuthUser != "1" {
		t.Errorf("AuthUser = %q, want %q", info.AuthUser, "1")
	}
	if info.XSRFToken != "xsrf123" {
		t.Errorf("XSRFToken = %q, want %q", info.XSRFToken, "xsrf123")
	}
	if info.GeminiBL != "bl_val" {
		t.Errorf("GeminiBL = %q, want %q", info.GeminiBL, "bl_val")
	}
}

func TestCookieCacheLoadPlainAndInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	plainPath := filepath.Join(tmpDir, "cookie.txt")
	if err := os.WriteFile(plainPath, []byte("SID=s1; SAPISID=sap_plain"), 0600); err != nil {
		t.Fatalf("write plain cookie file: %v", err)
	}
	info, err := NewCookieCache(plainPath).Load()
	if err != nil {
		t.Fatalf("Load() plain unexpected error: %v", err)
	}
	if info.SAPISID != "sap_plain" || info.Cookie != "SID=s1; SAPISID=sap_plain" {
		t.Errorf("unexpected plain CookieInfo: %+v", info)
	}

	badPath := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(badPath, []byte("[{invalid json"), 0600); err != nil {
		t.Fatalf("write bad cookie file: %v", err)
	}
	if _, err := NewCookieCache(badPath).Load(); err == nil {
		t.Fatal("expected error for malformed JSON array cookie file, got nil")
	}
}

func TestCookieCacheReloadsOnSameMtimeRewrite(t *testing.T) {
	tmpDir := t.TempDir()
	cookiePath := filepath.Join(tmpDir, "cookie.json")
	fixedTime := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	first := `[{"domain":".google.com","name":"SAPISID","value":"sap_v1"}]`
	if err := os.WriteFile(cookiePath, []byte(first), 0600); err != nil {
		t.Fatalf("write initial cookie file: %v", err)
	}
	if err := os.Chtimes(cookiePath, fixedTime, fixedTime); err != nil {
		t.Fatalf("chtimes initial: %v", err)
	}

	cache := NewCookieCache(cookiePath)
	info1, err := cache.Load()
	if err != nil {
		t.Fatalf("first Load() error: %v", err)
	}
	if info1.SAPISID != "sap_v1" {
		t.Fatalf("first SAPISID = %q, want sap_v1", info1.SAPISID)
	}

	// Rewrite with identical length and identical mtime to simulate atomic rename preserving timestamp.
	second := `[{"domain":".google.com","name":"SAPISID","value":"sap_v2"}]`
	if err := os.WriteFile(cookiePath, []byte(second), 0600); err != nil {
		t.Fatalf("rewrite cookie file: %v", err)
	}
	if err := os.Chtimes(cookiePath, fixedTime, fixedTime); err != nil {
		t.Fatalf("chtimes rewrite: %v", err)
	}

	info2, err := cache.Load()
	if err != nil {
		t.Fatalf("second Load() error: %v", err)
	}
	if info2.SAPISID != "sap_v2" {
		t.Errorf("expected reload when content changed with same mtime/size; got SAPISID = %q", info2.SAPISID)
	}
}
