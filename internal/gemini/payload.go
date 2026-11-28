package gemini

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func uuidV4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

const GeminiPayloadSize = 102

func applyPersistenceFlags(inner []any, cfg config.Config) {
	if cfg.TemporaryChats {
		inner[41] = []any{1}
		inner[45] = 1
	} else {
		inner[41] = []any{2}
	}
}

// BuildBody constructs a stateless form-encoded payload (`f.req`) expected by Gemini's frontend RPC endpoint.
func BuildBody(prompt string, modelID, thinkMode int, fileRefs []string, extra map[int]any, cfg config.Config) string {
	return BuildBodyWithMeta(prompt, modelID, thinkMode, fileRefs, extra, cfg, SessionMeta{})
}

// BuildBodyWithMeta constructs the form-encoded payload (`f.req`) with optional
// conversation metadata (cid, rid, rcid) at index 2 to continue an existing Gemini thread.
func BuildBodyWithMeta(prompt string, modelID, thinkMode int, fileRefs []string, extra map[int]any, cfg config.Config, meta SessionMeta) string {
	inner := make([]any, GeminiPayloadSize)

	if len(fileRefs) > 0 {
		refs := make([]any, len(fileRefs))
		for i, ref := range fileRefs {
			refs[i] = []any{nil, nil, ref}
		}
		inner[0] = []any{prompt, 0, nil, refs, nil, nil, 0}
	} else {
		inner[0] = []any{prompt, 0, nil, nil, nil, nil, 0}
	}

	inner[1] = []any{"en"}
	inner[2] = []any{meta.CID, meta.RID, meta.RCID, nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []any{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []any{[]any{thinkMode}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []any{4}
	applyPersistenceFlags(inner, cfg)
	inner[53] = 0
	inner[59] = uuidV4()
	inner[61] = []any{}
	inner[68] = 1
	inner[79] = modelID

	for k, v := range extra {
		if k >= 0 && k < len(inner) {
			inner[k] = v
		}
	}

	innerJSON, _ := json.Marshal(inner)
	outer := []any{nil, string(innerJSON)}
	outerJSON, _ := json.Marshal(outer)

	form := url.Values{}
	form.Set("f.req", string(outerJSON))
	if cfg.XSRFToken != "" {
		form.Set("at", cfg.XSRFToken)
	}

	return form.Encode()
}

// BuildURL constructs the Gemini API endpoint URL. If blOverride is non-empty, it is used instead of cfg.GeminiBL.
func BuildURL(cfg config.Config, blOverride string) string {
	reqid := time.Now().Unix() % 1000000
	prefix := AccountPrefix(cfg.AuthUser)
	bl := cfg.GeminiBL
	if blOverride != "" {
		bl = blOverride
	}
	return fmt.Sprintf(
		"https://gemini.google.com%s/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c",
		prefix,
		bl,
		reqid,
	)
}
