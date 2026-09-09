package gemini

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

var reBL = regexp.MustCompile(`boq_assistant-bard-web-server_\d+\.\d+_p\d+`)

// FetchLatestBL fetches the latest BL identifier from the Gemini frontend page.
// It makes a GET request to gemini.google.com/app with a 15s timeout,
// then extracts the boq_assistant-bard-web-server pattern from the HTML.
func FetchLatestBL(client Requester) (string, error) {
	reqURL := "https://gemini.google.com/app"
	req, err := http.NewRequestWithContext(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	// Use a short timeout for the BL fetch to avoid blocking startup too long.
	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("BL fetch request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("BL fetch read failed: %w", err)
	}

	html := string(bodyBytes)
	m := reBL.FindString(html)
	if m == "" {
		return "", fmt.Errorf("BL pattern not found in response")
	}
	return m, nil
}

// FetchLatestBLWithProxy fetches the latest BL using a proxy-aware HTTP client.
func FetchLatestBLWithProxy(client Requester, proxyURL string) (string, error) {
	reqURL := "https://gemini.google.com/app"
	req, err := http.NewRequestWithContext(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("BL fetch request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("BL fetch read failed: %w", err)
	}

	html := string(bodyBytes)
	m := reBL.FindString(html)
	if m == "" {
		return "", fmt.Errorf("BL pattern not found in response")
	}
	return m, nil
}
