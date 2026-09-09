package multimodal

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
)

func UploadImage(client gemini.Requester, tokens PageTokens, imgBytes []byte, mime string, cookieCache *gemini.CookieCache, authUser string) (string, error) {
	if mime == "" {
		mime = "image/png"
	}

	pushID := tokens.PushID
	if pushID == "" {
		pushID = DefaultPushID
	}
	pctx := tokens.Pctx
	if pctx == "" {
		pctx = DefaultPctx
	}

	cookieInfo, _ := cookieCache.Load()

	// Step 1: Initiate resumable upload
	startHeaders := make(http.Header)
	startHeaders.Set("Push-ID", pushID)
	startHeaders.Set("X-Tenant-Id", "bard-storage")
	startHeaders.Set("X-Client-Pctx", pctx)
	startHeaders.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprintf("%d", len(imgBytes)))
	startHeaders.Set("X-Goog-Upload-Header-Content-Type", mime)
	startHeaders.Set("X-Goog-Upload-Protocol", "resumable")
	startHeaders.Set("X-Goog-Upload-Command", "start")
	startHeaders.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	startHeaders.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	if cookieInfo.Cookie != "" {
		startHeaders.Set("Cookie", cookieInfo.Cookie)
	}
	if cookieInfo.SAPISID != "" {
		startHeaders.Set("Authorization", gemini.SAPISIDHash(cookieInfo.SAPISID))
	}

	startURL := "https://content-push.googleapis.com/upload/"
	req1, err := http.NewRequest("POST", startURL, bytes.NewReader(nil))
	if err != nil {
		return "", err
	}
	req1.Header = startHeaders

	resp1, err := client.Do(req1)
	if err != nil {
		return "", fmt.Errorf("Upload step 1 failed: %w", err)
	}
	defer resp1.Body.Close()

	uploadURL := resp1.Header.Get("X-Goog-Upload-URL")
	if uploadURL == "" {
		uploadURL = resp1.Header.Get("x-goog-upload-url")
	}
	if uploadURL == "" {
		return "", fmt.Errorf("No upload URL in response headers")
	}

	// Step 2: Upload file data + finalize
	uploadHeaders := make(http.Header)
	uploadHeaders.Set("X-Goog-Upload-Command", "upload, finalize")
	uploadHeaders.Set("X-Goog-Upload-Offset", "0")
	uploadHeaders.Set("Content-Type", "application/octet-stream")
	uploadHeaders.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	req2, err := http.NewRequest("POST", uploadURL, bytes.NewReader(imgBytes))
	if err != nil {
		return "", err
	}
	req2.Header = uploadHeaders

	resp2, err := client.Do(req2)
	if err != nil {
		return "", fmt.Errorf("Upload step 2 failed: %w", err)
	}
	defer resp2.Body.Close()

	bodyBytes, err := io.ReadAll(resp2.Body)
	if err != nil {
		return "", err
	}

	fileRef := strings.TrimSpace(string(bodyBytes))
	if fileRef == "" || !strings.HasPrefix(fileRef, "/") {
		return "", fmt.Errorf("Invalid file reference: %s", fileRef)
	}

	return fileRef, nil
}

func FetchImageBytes(client gemini.Requester, imageURL string) ([]byte, error) {
	// SSRF guard: only allow http/https schemes (parsed, not prefix-matched).
	parsed, err := url.Parse(strings.TrimSpace(imageURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid image URL scheme, only http/https allowed")
	}

	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Image fetch failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}
