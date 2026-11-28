package gemini

import (
	"errors"
	"fmt"
	"strings"
)

type StreamParser struct {
	prevText string
	buf      string
	meta     SessionMeta
}

func NewStreamParser() *StreamParser {
	return &StreamParser{}
}

// Meta returns the latest Gemini conversation metadata observed in the stream.
func (p *StreamParser) Meta() SessionMeta {
	return p.meta
}

// ResetBuffer clears the line buffer before a retry attempt while preserving prevText across retries.
func (p *StreamParser) ResetBuffer() {
	p.buf = ""
}

func (p *StreamParser) Feed(chunk string) ([]string, error) {
	p.buf += chunk

	if strings.Contains(p.buf, "BardErrorInfo") {
		if code, ok := IsBardError(p.buf); ok {
			return nil, fmt.Errorf("gemini upstream rejected request: BardErrorInfo [%s]", code)
		}
	}

	var deltas []string
	for strings.Contains(p.buf, "\n") {
		idx := strings.Index(p.buf, "\n")
		line := p.buf[:idx]
		p.buf = p.buf[idx+1:]

		texts, lineMeta := ExtractLineResult(line)
		if lineMeta.CID != "" {
			p.meta.CID = lineMeta.CID
		}
		if lineMeta.RID != "" {
			p.meta.RID = lineMeta.RID
		}
		if lineMeta.RCID != "" {
			p.meta.RCID = lineMeta.RCID
		}
		for _, t := range texts {
			// Skip if this text segment has already been fully processed or is identical to the last one.
			if t == p.prevText || strings.HasPrefix(p.prevText, t) {
				continue
			}

			// If the new text doesn't start with the previously processed text, it means
			// the server sent a completely different text blob, breaking the delta continuation assumption.
			if !strings.HasPrefix(t, p.prevText) {
				return nil, errors.New("gemini stream content changed during retry")
			}

			delta := CleanText(t[len(p.prevText):], false)
			p.prevText = t
			if delta != "" {
				deltas = append(deltas, delta)
			}
		}
	}

	return deltas, nil
}
