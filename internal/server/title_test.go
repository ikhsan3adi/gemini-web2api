package server

import (
	"encoding/json"
	"testing"
)

func TestIsTitleSummaryRequest(t *testing.T) {
	prompt := `[System instruction]: Generate a concise, sentence-case title (3-7 words) that captures the main topic or goal of this session. The title should be clear enough that the user recognizes the session in a list. Use sentence case: capitalize only the first word and proper nouns.

Respond with EXACTLY one JSON object and nothing else:
{"isNewTopic": boolean, "title": string}

<session>
whats douyin
</session>`

	isTitle, session := isTitleSummaryRequest(prompt)
	if !isTitle {
		t.Fatalf("expected isTitleSummaryRequest to return true")
	}
	if session != "whats douyin" {
		t.Fatalf("expected session 'whats douyin', got '%s'", session)
	}

	titleJSON := generateFastTitleJSON(session)
	var parsed struct {
		IsNewTopic bool   `json:"isNewTopic"`
		Title      string `json:"title"`
	}
	if err := json.Unmarshal([]byte(titleJSON), &parsed); err != nil {
		t.Fatalf("failed to parse titleJSON: %v", err)
	}
	if !parsed.IsNewTopic {
		t.Errorf("expected isNewTopic true")
	}
	if parsed.Title != "Explanation of Douyin" {
		t.Errorf("expected 'Explanation of Douyin', got '%s'", parsed.Title)
	}
}

func TestNormalPromptNotTitle(t *testing.T) {
	normalPrompt := "# Tool Use\n\n[User]: whats douyin"
	isTitle, _ := isTitleSummaryRequest(normalPrompt)
	if isTitle {
		t.Errorf("expected normal prompt not to be classified as title request")
	}
}
