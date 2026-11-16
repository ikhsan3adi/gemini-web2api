package server

import (
	"fmt"
	"strings"
)

// isTitleSummaryRequest checks if the incoming prompt is a WorkBuddy or similar client's
// automatic session title or topic generation request e.g.
// "[System instruction]: Generate a concise, sentence-case title (3-7 words)... <session> whats douyin </session>"
func isTitleSummaryRequest(prompt string) (bool, string) {
	lower := strings.ToLower(prompt)
	if (strings.Contains(lower, "generate a concise, sentence-case title") ||
		strings.Contains(lower, "generate a concise title") ||
		strings.Contains(lower, "title (3-7 words)") ||
		strings.Contains(lower, "captures the main topic or goal of this session")) &&
		strings.Contains(lower, "isnewtopic") {

		sessionText := ""
		start := strings.Index(lower, "<session>")
		end := strings.Index(lower, "</session>")
		if start != -1 && end != -1 && end > start && end <= len(prompt) {
			sessionText = strings.TrimSpace(prompt[start+len("<session>") : end])
		} else {
			// Fallback: look for the last non-instruction line
			lines := strings.Split(prompt, "\n")
			for i := len(lines) - 1; i >= 0; i-- {
				l := strings.TrimSpace(lines[i])
				if l != "" && !strings.HasPrefix(l, "[") && !strings.HasPrefix(l, "{") && !strings.HasPrefix(l, "<") {
					sessionText = l
					break
				}
			}
		}
		return true, sessionText
	}
	return false, ""
}

// generateFastTitleJSON produces an immediate valid JSON title response for WorkBuddy title requests.
// e.g. `{"isNewTopic": true, "title": "Explanation of Douyin"}`
func generateFastTitleJSON(sessionContent string) string {
	clean := strings.TrimSpace(sessionContent)

	cleanLower := strings.ToLower(clean)
	starters := []string{
		"what is ", "whats ", "who is ", "how to ", "how do i ", "can you ", "tell me about ",
		"explain ", "why is ", "what are ",
	}
	for _, starter := range starters {
		if strings.HasPrefix(cleanLower, starter) {
			clean = strings.TrimSpace(clean[len(starter):])
			cleanLower = strings.ToLower(clean)
			break
		}
	}

	clean = strings.TrimRight(clean, "?!.,:;")

	words := strings.Fields(clean)
	if len(words) > 5 {
		words = words[:5]
	}
	for i, w := range words {
		runes := []rune(w)
		if len(runes) > 0 {
			words[i] = strings.ToUpper(string(runes[:1])) + strings.ToLower(string(runes[1:]))
		}
	}

	mainTopic := strings.Join(words, " ")
	var title string
	if mainTopic == "" {
		title = "New Session"
	} else if len(words) == 1 {
		title = "Explanation of " + mainTopic
	} else {
		title = mainTopic
	}

	return fmt.Sprintf(`{"isNewTopic": true, "title": %q}`, title)
}
