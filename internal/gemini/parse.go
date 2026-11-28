package gemini

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	reCodeExec    = regexp.MustCompile(`(?s)\x60\x60\x60(?:python|javascript|text)\?code_(?:reference|stdout)&code_event_index=\d+\n.*?\x60\x60\x60\n?`)
	reCardContent = regexp.MustCompile(`http://googleusercontent\.com/card_content/\d+\n?`)
	reBardError   = regexp.MustCompile(`BardErrorInfo\s*\[(\d+)\]`)
)

func CleanText(text string, strip bool) string {
	text = reCodeExec.ReplaceAllString(text, "")
	text = reCardContent.ReplaceAllString(text, "")
	if strip {
		return strings.TrimSpace(text)
	}
	return text
}

// SessionMeta holds Gemini Web's server-side conversation identifiers
// returned in StreamGenerate responses and passed back in inner[2] to continue a thread.
type SessionMeta struct {
	CID  string
	RID  string
	RCID string
}

func (m SessionMeta) IsZero() bool {
	return m.CID == "" && m.RID == "" && m.RCID == ""
}

func ExtractTextsFromLine(line string) []string {
	texts, _ := ExtractLineResult(line)
	return texts
}

// ExtractLineResult parses a single StreamGenerate response line and returns both
// candidate text fragments and any conversation metadata (cid, rid, rcid).
func ExtractLineResult(line string) ([]string, SessionMeta) {
	var meta SessionMeta
	if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
		return nil, meta
	}

	var arr []any
	if err := json.Unmarshal([]byte(line), &arr); err != nil || len(arr) == 0 {
		return nil, meta
	}

	firstElem, ok := arr[0].([]any)
	if !ok || len(firstElem) < 3 {
		return nil, meta
	}

	innerStr, ok := firstElem[2].(string)
	if !ok || len(innerStr) < 50 {
		return nil, meta
	}

	var inner []any
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
		return nil, meta
	}

	if len(inner) > 1 {
		if ids, ok := inner[1].([]any); ok && len(ids) >= 2 {
			meta.CID, _ = ids[0].(string)
			meta.RID, _ = ids[1].(string)
		}
	}

	if len(inner) <= 4 || inner[4] == nil {
		return nil, meta
	}

	parts, ok := inner[4].([]any)
	if !ok {
		return nil, meta
	}

	var texts []string
	for _, part := range parts {
		partList, ok := part.([]any)
		if !ok {
			continue
		}
		if meta.RCID == "" && len(partList) > 0 {
			meta.RCID, _ = partList[0].(string)
		}
		if len(partList) > 1 && partList[1] != nil {
			tList, ok := partList[1].([]any)
			if ok {
				for _, t := range tList {
					if str, ok := t.(string); ok && str != "" {
						texts = append(texts, str)
					}
				}
			}
		}
	}

	return texts, meta
}

func IsBardError(raw string) (string, bool) {
	match := reBardError.FindStringSubmatch(raw)
	if len(match) > 1 {
		return match[1], true
	}
	return "", false
}

func ExtractResponseText(raw string) (string, error) {
	text, _, err := ExtractResponseWithMeta(raw)
	return text, err
}

// ExtractResponseWithMeta extracts the longest cleaned response text and
// the latest conversation metadata from a buffered StreamGenerate response.
func ExtractResponseWithMeta(raw string) (string, SessionMeta, error) {
	if code, ok := IsBardError(raw); ok {
		return "", SessionMeta{}, fmt.Errorf("gemini upstream rejected request: BardErrorInfo [%s]", code)
	}

	var lastText string
	var meta SessionMeta
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		texts, lineMeta := ExtractLineResult(line)
		if lineMeta.CID != "" {
			meta.CID = lineMeta.CID
		}
		if lineMeta.RID != "" {
			meta.RID = lineMeta.RID
		}
		if lineMeta.RCID != "" {
			meta.RCID = lineMeta.RCID
		}
		for _, t := range texts {
			if len(t) > len(lastText) {
				lastText = t
			}
		}
	}

	return CleanText(lastText, true), meta, nil
}
