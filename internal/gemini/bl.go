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
// It issues the GET through the provided Requester so proxy/TLS configuration
// is honored, with a 15s context timeout to avoid blocking startup too long,
// then extracts the boq_assistant-bard-web-server pattern from the HTML.
func FetchLatestBL(client Requester) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://gemini.google.com/app", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("BL fetch request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("BL fetch read failed: %w", err)
	}

	m := reBL.FindString(string(bodyBytes))
	if m == "" {
		return "", fmt.Errorf("BL pattern not found in response")
	}
	return m, nil
}
