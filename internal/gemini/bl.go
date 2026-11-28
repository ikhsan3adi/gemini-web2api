package gemini

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

var (
	reBL     = regexp.MustCompile(`boq_(?:gemini-web-uiserver|assistant-bard-web-server)_\d+\.\d+_p\d+`)
	reSNlM0e = regexp.MustCompile(`"SNlM0e":"([^"]+)"`)
)

// FetchLatestBL fetches the latest BL identifier from the Gemini frontend page.
// It issues the GET through the provided Requester so proxy/TLS configuration
// is honored, with a 15s context timeout to avoid blocking startup too long,
// then extracts the BL pattern from the HTML.
func FetchLatestBL(client Requester) (string, error) {
	bl, _, err := FetchSessionParams(client, "", "")
	if err != nil {
		return "", err
	}
	if bl == "" {
		return "", fmt.Errorf("BL pattern not found in response")
	}
	return bl, nil
}

// FetchSessionParams fetches the Gemini frontend page and extracts the latest BL
// identifier and, when authenticated cookies are supplied, the SNlM0e XSRF token.
func FetchSessionParams(client Requester, cookieHeader, authUser string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reqURL := fmt.Sprintf("https://gemini.google.com%s/app", AccountPrefix(authUser))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("BL fetch request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("BL fetch read failed: %w", err)
	}

	html := string(bodyBytes)
	bl := reBL.FindString(html)
	var xsrf string
	if m := reSNlM0e.FindStringSubmatch(html); len(m) > 1 {
		xsrf = m[1]
	}
	return bl, xsrf, nil
}
