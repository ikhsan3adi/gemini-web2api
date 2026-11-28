package gemini

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type CookieInfo struct {
	Cookie    string
	SAPISID   string
	AuthUser  string
	XSRFToken string
	GeminiBL  string
}

type CookieCache struct {
	mu    sync.Mutex
	file  string
	mtime time.Time
	size  int64
	hash  [32]byte
	info  CookieInfo
}

func NewCookieCache(file string) *CookieCache {
	return &CookieCache{file: file}
}

func (c *CookieCache) Load() (CookieInfo, error) {
	if c.file == "" {
		return CookieInfo{}, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	stat, err := os.Stat(c.file)
	if err != nil {
		if c.info.Cookie != "" {
			log.Printf("Cookie load error: %v", err)
			return c.info, nil
		}
		return CookieInfo{}, err
	}

	contentBytes, err := os.ReadFile(c.file)
	if err != nil {
		if c.info.Cookie != "" {
			log.Printf("Cookie load error: %v", err)
			return c.info, nil
		}
		return CookieInfo{}, err
	}

	sum := sha256.Sum256(contentBytes)
	if stat.ModTime().Equal(c.mtime) && stat.Size() == c.size && sum == c.hash && c.info.Cookie != "" {
		return c.info, nil
	}

	content := strings.TrimSpace(string(contentBytes))
	info, err := parseCookieContent(content)
	if err != nil {
		if c.info.Cookie != "" {
			log.Printf("Cookie load error: %v", err)
			return c.info, nil
		}
		return CookieInfo{}, err
	}

	c.mtime = stat.ModTime()
	c.size = stat.Size()
	c.hash = sum
	c.info = info

	return c.info, nil
}

type browserCookie struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	Value  string `json:"value"`
}

func isGoogleCookieDomain(domain string) bool {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	return d == "" || d == "google.com" || strings.HasSuffix(d, ".google.com")
}

func parseCookieContent(content string) (CookieInfo, error) {
	var info CookieInfo

	switch {
	case strings.HasPrefix(content, "{"):
		var data struct {
			Cookie    string          `json:"cookie"`
			SAPISID   string          `json:"sapisid"`
			AuthUser  json.RawMessage `json:"auth_user"`
			XSRFToken *string         `json:"xsrf_token"`
			GeminiBL  *string         `json:"gemini_bl"`
		}
		if err := json.Unmarshal([]byte(content), &data); err != nil {
			return CookieInfo{}, fmt.Errorf("invalid cookie JSON object: %w", err)
		}
		info.Cookie = data.Cookie
		info.SAPISID = data.SAPISID
		if len(data.AuthUser) > 0 {
			u := strings.Trim(strings.TrimSpace(string(data.AuthUser)), `"`)
			if u != "null" {
				info.AuthUser = u
			}
		}
		if data.XSRFToken != nil {
			info.XSRFToken = *data.XSRFToken
		}
		if data.GeminiBL != nil {
			info.GeminiBL = *data.GeminiBL
		}

	case strings.HasPrefix(content, "["):
		var cookies []browserCookie
		if err := json.Unmarshal([]byte(content), &cookies); err != nil {
			return CookieInfo{}, fmt.Errorf("invalid cookie JSON array: %w", err)
		}
		sanitizer := strings.NewReplacer("\r", "", "\n", "")
		pairs := make([]string, 0, len(cookies))
		idxByName := make(map[string]int, len(cookies))
		for _, bc := range cookies {
			if !isGoogleCookieDomain(bc.Domain) {
				continue
			}
			name := strings.TrimSpace(bc.Name)
			if name == "" {
				continue
			}
			val := sanitizer.Replace(bc.Value)
			pair := name + "=" + val
			if idx, exists := idxByName[name]; exists {
				pairs[idx] = pair
			} else {
				idxByName[name] = len(pairs)
				pairs = append(pairs, pair)
			}
		}
		info.Cookie = strings.Join(pairs, "; ")

	default:
		info.Cookie = content
	}

	if info.SAPISID == "" && info.Cookie != "" {
		for _, pair := range strings.Split(info.Cookie, ";") {
			parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
			if len(parts) == 2 && parts[0] == "SAPISID" {
				info.SAPISID = parts[1]
				break
			}
		}
	}

	return info, nil
}

func SAPISIDHash(sapisid string) string {
	if sapisid == "" {
		return ""
	}
	ts := time.Now().Unix()
	input := fmt.Sprintf("%d %s https://gemini.google.com", ts, sapisid)
	h := sha1.Sum([]byte(input))
	return fmt.Sprintf("SAPISIDHASH %d_%s", ts, hex.EncodeToString(h[:]))
}

func AccountPrefix(authUser string) string {
	if authUser == "" {
		return ""
	}
	return "/u/" + authUser
}
