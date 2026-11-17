package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

const (
	maxBridgeAttachmentBytes = 25 << 20 // 25 MB per file
	maxLocalAutoUploadFiles  = 5        // max auto-detected local files per turn
)

// allowedBinaryDocExts are always auto-uploaded when referenced on localhost (> 0 bytes).
var allowedBinaryDocExts = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".doc":  "application/msword",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".ppt":  "application/vnd.ms-powerpoint",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xls":  "application/vnd.ms-excel",
	".csv":  "text/csv",
	".rtf":  "application/rtf",
	".odt":  "application/vnd.oasis.opendocument.text",
	".epub": "application/epub+zip",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".bmp":  "image/bmp",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".m4a":  "audio/mp4",
}

// allowedCodeTextExts are auto-uploaded verbatim when their size >= largeCodeMinBytes
// (eliminating chunk-by-chunk read_file tool loops on long code files).
var allowedCodeTextExts = map[string]string{
	".go":       "text/plain",
	".py":       "text/plain",
	".js":       "text/javascript",
	".mjs":      "text/javascript",
	".cjs":      "text/javascript",
	".ts":       "text/plain",
	".tsx":      "text/plain",
	".jsx":      "text/plain",
	".java":     "text/plain",
	".kt":       "text/plain",
	".kts":      "text/plain",
	".cs":       "text/plain",
	".cpp":      "text/plain",
	".cc":       "text/plain",
	".cxx":      "text/plain",
	".c":        "text/plain",
	".h":        "text/plain",
	".hpp":      "text/plain",
	".rs":       "text/plain",
	".rb":       "text/plain",
	".php":      "text/plain",
	".swift":    "text/plain",
	".scala":    "text/plain",
	".dart":     "text/plain",
	".lua":      "text/plain",
	".r":        "text/plain",
	".sh":       "text/plain",
	".bash":     "text/plain",
	".zsh":      "text/plain",
	".ps1":      "text/plain",
	".bat":      "text/plain",
	".sql":      "text/plain",
	".html":     "text/html",
	".htm":      "text/html",
	".css":      "text/css",
	".scss":     "text/plain",
	".sass":     "text/plain",
	".less":     "text/plain",
	".vue":      "text/plain",
	".svelte":   "text/plain",
	".json":     "application/json",
	".jsonl":    "text/plain",
	".yaml":     "text/yaml",
	".yml":      "text/yaml",
	".toml":     "text/plain",
	".xml":      "application/xml",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".txt":      "text/plain",
	".log":      "text/plain",
	".proto":    "text/plain",
	".graphql":  "text/plain",
	".tf":       "text/plain",
	".gradle":   "text/plain",
	".cmake":    "text/plain",
	".ini":      "text/plain",
	".cfg":      "text/plain",
}

const allowedExtPattern = `(?:pdf|docx|doc|pptx|ppt|xlsx|xls|csv|rtf|odt|epub|png|jpe?g|webp|gif|bmp|mp3|wav|m4a|go|py|mjs|cjs|js|tsx|ts|jsx|java|kts|kt|cs|cpp|cc|cxx|c|hpp|h|rs|rb|php|swift|scala|dart|lua|r|sh|bash|zsh|ps1|bat|sql|html|htm|css|scss|sass|less|vue|svelte|jsonl|json|yaml|yml|toml|xml|markdown|md|txt|log|proto|graphql|tf|gradle|cmake|ini|cfg)`

var (
	reQuotedPath   = regexp.MustCompile("(?i)[\"'`]" + `([^"'` + "`" + `\r\n]+?\.` + allowedExtPattern + `)` + "[\"'`]")
	reFileURI      = regexp.MustCompile(`(?i)file://([^\s"'<>` + "`" + `]+?\.` + allowedExtPattern + `)`)
	reWindowsPath  = regexp.MustCompile(`(?i)([A-Za-z]:[\\/][^\r\n"'<>|*?\t` + "`" + `]+?\.` + allowedExtPattern + `)`)
	reUnixAbsPath  = regexp.MustCompile(`(?i)(?:^|[\s(\['"@,;=])(/(?:[^\s"'<>|*?` + "`" + `]+/)*[^\s"'<>|*?` + "`" + `]+\.` + allowedExtPattern + `)`)
	reRelativePath = regexp.MustCompile(`(?i)(?:^|[\s(\['"@,:;=])((?:[^\s"'<>|*?/:\\` + "`" + `]+[\\/])*[^\s"'<>|*?/:\\@` + "`" + `]+\.` + allowedExtPattern + `)`)
	reWorkspaceDir = regexp.MustCompile(`(?im)(?:working\s+directory|workspace(?:\s+(?:path|dir|directory|root))?|current\s+(?:working\s+)?directory|cwd)\s*[:=]\s*["'` + "`" + `]?([^\r\n"'` + "`" + `<>]+)["'` + "`" + `]?|<cwd>([^<]+)</cwd>`)
)

// isLoopbackRequest returns true only for direct localhost/loopback HTTP clients (never proxied).
func isLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ExtractWorkspaceDir looks for a working directory hint in system or user messages
// (e.g. "Working directory: C:\Users\..." or "<cwd>/home/user/project</cwd>").
func ExtractWorkspaceDir(messages []models.OpenAIMessage) string {
	for _, msg := range messages {
		if msg.Role != "system" && msg.Role != "developer" && msg.Role != "user" {
			continue
		}
		text, _ := format.ExtractMessageContent(msg)
		if text == "" {
			continue
		}
		matches := reWorkspaceDir.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			cand := strings.TrimSpace(m[1])
			if cand == "" && len(m) > 2 {
				cand = strings.TrimSpace(m[2])
			}
			cand = strings.TrimRight(cand, ".,;)")
			if cand == "" {
				continue
			}
			if info, err := os.Stat(cand); err == nil && info.IsDir() {
				return cand
			}
		}
	}
	return ""
}

// ExtractCandidatePaths extracts candidate local document, media, or code file paths mentioned in text.
func ExtractCandidatePaths(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`+"`()[]<>,.;:")
		p = strings.TrimPrefix(p, "@")
		if p == "" {
			return
		}
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			return
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	for _, m := range reQuotedPath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reFileURI.FindAllStringSubmatch(text, -1) {
		raw := m[1]
		if unesc, err := url.PathUnescape(raw); err == nil {
			raw = unesc
		}
		if len(raw) >= 3 && raw[0] == '/' && raw[2] == ':' {
			raw = raw[1:]
		}
		add(raw)
	}
	for _, m := range reWindowsPath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reUnixAbsPath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reRelativePath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}

	return out
}

func isSensitiveFilename(base string) bool {
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, ".") {
		return true
	}
	for _, bad := range []string{"cookie", "secret", "credential", "password", "id_rsa", "private_key", "token"} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

func fileVersionSignature(info os.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func resolveAndReadLocalFile(candidate string, baseDirs []string, largeCodeMinBytes int) (absPath string, sig string, att bridge.Attachment, ok bool) {
	ext := strings.ToLower(filepath.Ext(candidate))
	mime, isDocOrMedia := allowedBinaryDocExts[ext]
	isCodeText := false
	if !isDocOrMedia {
		mime, isCodeText = allowedCodeTextExts[ext]
		if !isCodeText || largeCodeMinBytes <= 0 {
			return "", "", bridge.Attachment{}, false
		}
	}

	var tryPaths []string
	if filepath.IsAbs(candidate) {
		tryPaths = append(tryPaths, candidate)
	} else {
		for _, dir := range baseDirs {
			if dir != "" {
				tryPaths = append(tryPaths, filepath.Join(dir, candidate))
			}
		}
	}

	for _, p := range tryPaths {
		cleaned, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			continue
		}
		base := filepath.Base(cleaned)
		if isSensitiveFilename(base) {
			continue
		}
		info, err := os.Stat(cleaned)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() <= 0 || info.Size() > maxBridgeAttachmentBytes {
			continue
		}
		if isCodeText && info.Size() < int64(largeCodeMinBytes) {
			// Keep short code files inline via normal agent tools; auto-upload only long code files.
			continue
		}
		data, err := os.ReadFile(cleaned)
		if err != nil || len(data) == 0 {
			continue
		}
		src := "local_auto"
		if isCodeText {
			src = "local_large_code"
		}
		return cleaned, fileVersionSignature(info), bridge.Attachment{
			Filename:   base,
			MIME:       mime,
			DataBase64: base64.StdEncoding.EncodeToString(data),
			Size:       len(data),
			Source:     src,
		}, true
	}

	return "", "", bridge.Attachment{}, false
}

// DiscoverLocalUploadFiles inspects the latest message (when it is a user turn) for references
// to existing local PDFs, Office documents, media files, or long source code files
// (>= largeCodeMinBytes) and reads them for direct upload through the browser bridge.
// Files already uploaded with the same size+mtime signature in this tab session are skipped.
func DiscoverLocalUploadFiles(messages []models.OpenAIMessage, alreadyUploaded map[string]string, largeCodeMinBytes int) ([]bridge.Attachment, map[string]string) {
	if len(messages) == 0 {
		return nil, nil
	}
	last := messages[len(messages)-1]
	if last.Role != "user" && last.Role != "" {
		return nil, nil
	}
	latestUserText, _ := format.ExtractMessageContent(last)
	if strings.TrimSpace(latestUserText) == "" {
		return nil, nil
	}

	candidates := ExtractCandidatePaths(latestUserText)
	if len(candidates) == 0 {
		return nil, nil
	}

	var baseDirs []string
	if ws := ExtractWorkspaceDir(messages); ws != "" {
		baseDirs = append(baseDirs, ws)
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		baseDirs = append(baseDirs, cwd)
	}

	seenAbs := make(map[string]bool)
	var attachments []bridge.Attachment
	uploadedSigs := make(map[string]string)

	for _, cand := range candidates {
		if len(attachments) >= maxLocalAutoUploadFiles {
			break
		}
		absPath, sig, att, ok := resolveAndReadLocalFile(cand, baseDirs, largeCodeMinBytes)
		if !ok {
			continue
		}
		if seenAbs[absPath] {
			continue
		}
		if alreadyUploaded != nil && alreadyUploaded[absPath] == sig {
			continue
		}
		seenAbs[absPath] = true
		attachments = append(attachments, att)
		uploadedSigs[absPath] = sig
	}

	return attachments, uploadedSigs
}

// SpilloverOversizedPrompt moves oversized code / tool-result prompts (> maxChars)
// into a UTF-8 file attachment so Gemini receives 100% verbatim text without hitting
// browser composer character limits or triggering giant DOM paste slowdowns.
func SpilloverOversizedPrompt(prompt string, maxChars int, isDelta bool) (compactPrompt string, att *bridge.Attachment) {
	if maxChars <= 0 || len(prompt) <= maxChars {
		return prompt, nil
	}

	// On a full prompt that starts with # Tool Use, keep the # Tool Use header inline
	// (if reasonably sized) and spill only the oversized conversation/code body to a file.
	if !isDelta && strings.HasPrefix(prompt, "# Tool Use") {
		if availIdx := strings.Index(prompt, "\nAvailable tools:\n"); availIdx != -1 {
			afterAvail := availIdx + len("\nAvailable tools:\n")
			if splitRel := strings.Index(prompt[afterAvail:], "\n\n"); splitRel != -1 {
				headerEnd := afterAvail + splitRel
				header := prompt[:headerEnd]
				body := strings.TrimSpace(prompt[headerEnd+2:])
				if len(body) > maxChars/2 && len(header) <= maxChars {
					data := []byte(body)
					if len(data) <= maxBridgeAttachmentBytes {
						filename := "code_context.txt"
						return fmt.Sprintf(
								"%s\n\n[The full prompt / code context (%d chars) is attached directly in %s. Read the attached file verbatim in full before responding or calling tools.]",
								header, len(body), filename,
							), &bridge.Attachment{
								Filename:   filename,
								MIME:       "text/plain",
								DataBase64: base64.StdEncoding.EncodeToString(data),
								Size:       len(data),
								Source:     "spillover",
							}
					}
				}
			}
		}
	}

	// Delta turns (e.g. massive [Tool result for read_file]) or oversized single prompts:
	data := []byte(prompt)
	if len(data) > maxBridgeAttachmentBytes {
		return prompt, nil
	}
	filename := "tool_output_context.txt"
	if !isDelta {
		filename = "full_prompt_context.txt"
	}
	compact := fmt.Sprintf(
		"[Full tool output / code context (%d chars) is attached directly in %s. Read the attached file verbatim in full and continue — do NOT re-read it with local tools.]",
		len(prompt), filename,
	)
	return compact, &bridge.Attachment{
		Filename:   filename,
		MIME:       "text/plain",
		DataBase64: base64.StdEncoding.EncodeToString(data),
		Size:       len(data),
		Source:     "spillover",
	}
}

// FormatAttachmentsToBridge converts explicit API attachments (image_url, file, input_file, document)
// into bridge.Attachment payloads for the browser extension.
func FormatAttachmentsToBridge(items []format.Image, fetchRemote func(string) ([]byte, error)) []bridge.Attachment {
	if len(items) == 0 {
		return nil
	}

	var out []bridge.Attachment
	for idx, item := range items {
		data := item.Data
		mime := item.MIME

		if len(data) == 0 && item.URL != "" && fetchRemote != nil {
			fetched, err := fetchRemote(item.URL)
			if err != nil || len(fetched) == 0 {
				continue
			}
			data = fetched
		}

		if len(data) == 0 || len(data) > maxBridgeAttachmentBytes {
			continue
		}
		if mime == "" {
			if len(data) >= 4 && string(data[:4]) == "%PDF" {
				mime = "application/pdf"
			} else {
				mime = multimodal.DetectImageMime(data)
			}
		}

		filename := strings.TrimSpace(item.Filename)
		if filename == "" {
			filename = fmt.Sprintf("attachment_%d%s", idx+1, extForMIME(mime))
		}

		out = append(out, bridge.Attachment{
			Filename:   filename,
			MIME:       mime,
			DataBase64: base64.StdEncoding.EncodeToString(data),
			Size:       len(data),
			Source:     "api",
		})
	}
	return out
}

func extForMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "application/pdf":
		return ".pdf"
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "text/csv":
		return ".csv"
	case "text/plain":
		return ".txt"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	default:
		return ".bin"
	}
}

// ConversationFingerprints returns a deterministic slice of SHA-256 fingerprints
// starting with the tool-definition digest followed by one digest per message.
func ConversationFingerprints(req models.OpenAIChatRequest) []string {
	fps := make([]string, 0, len(req.Messages)+1)

	toolsHash := sha256.New()
	if len(req.Tools) > 0 {
		if b, err := json.Marshal(req.Tools); err == nil {
			toolsHash.Write(b)
		}
	}
	fps = append(fps, "tools:"+hex.EncodeToString(toolsHash.Sum(nil)))

	for _, msg := range req.Messages {
		h := sha256.New()
		h.Write([]byte(msg.Role))
		h.Write([]byte{0})
		h.Write([]byte(msg.Name))
		h.Write([]byte{0})
		text, atts := format.ExtractMessageContent(msg)
		h.Write([]byte(text))
		for _, att := range atts {
			h.Write([]byte{1})
			h.Write([]byte(att.Filename))
			h.Write([]byte(att.MIME))
			h.Write([]byte(att.URL))
			if len(att.Data) > 0 {
				sum := sha256.Sum256(att.Data)
				h.Write(sum[:])
			}
		}
		for _, tc := range msg.ToolCalls {
			h.Write([]byte{2})
			h.Write([]byte(tc.Function.Name))
			h.Write([]byte(tc.Function.Arguments))
		}
		fps = append(fps, hex.EncodeToString(h.Sum(nil)))
	}
	return fps
}

type preparedBridgeTurn struct {
	Prompt        string
	Attachments   []bridge.Attachment
	SessionClient string
	SessionConv   string
	Fingerprints  []string
	UploadedSigs  map[string]string
}

func (a *App) prepareBridgeRequest(r *http.Request, req models.OpenAIChatRequest, fullPrompt string, apiAttachments []format.Image) preparedBridgeTurn {
	sessClient, sessConvGen, alreadyUploaded := a.Bridge.ActiveSession()
	fingerprints := ConversationFingerprints(req)

	bridgePrompt := fullPrompt
	activeAtts := apiAttachments
	isDelta := false

	// Delta-context optimization: if the active browser tab already holds the exact
	// tool definitions and leading conversation turns, send only the newly appended turns.
	if a.Cfg.BridgeDeltaContext {
		matchedPrefix := a.Bridge.MatchDeltaPrefix(fingerprints)
		// Element 0 is the tools digest, so matchedPrefix > 1 means at least 1 message matched.
		if matchedPrefix > 1 {
			matchedMsgs := matchedPrefix - 1
			if matchedMsgs < len(req.Messages) {
				deltaPrompt, deltaAtts := format.MessagesToDeltaPrompt(req.Messages[matchedMsgs:], req.ToolChoice)
				if strings.TrimSpace(deltaPrompt) != "" {
					bridgePrompt = deltaPrompt
					activeAtts = deltaAtts
					isDelta = true
					a.Logf("Bridge delta context active: sending %d/%d new message(s) (%d chars vs %d full)",
						len(req.Messages)-matchedMsgs, len(req.Messages), len(deltaPrompt), len(fullPrompt))
				}
			}
		}
	}

	var requester gemini.Requester
	if a.Gem != nil {
		requester = a.Gem.HTTP
	}
	if requester == nil && a.HTTPClient != nil {
		requester = a.HTTPClient
	}
	var fetchFn func(string) ([]byte, error)
	if requester != nil {
		fetchFn = func(rawURL string) ([]byte, error) {
			return multimodal.FetchImageBytes(requester, rawURL)
		}
	}

	attachments := FormatAttachmentsToBridge(activeAtts, fetchFn)
	var uploadedSigs map[string]string

	// Local file auto-upload: for localhost requests (e.g. WorkBuddy on the same machine),
	// automatically attach referenced local PDFs/Office docs/images AND long source code files
	// (>= bridge_large_code_upload_bytes) directly to the Gemini tab so Gemini reads them natively.
	if a.Cfg.BridgeAutoUploadLocalFiles && isLoopbackRequest(r) {
		localAtts, discoveredSigs := DiscoverLocalUploadFiles(req.Messages, alreadyUploaded, a.Cfg.BridgeLargeCodeUploadBytes)
		if len(localAtts) > 0 {
			var names []string
			for _, att := range localAtts {
				names = append(names, fmt.Sprintf("%s (%d KB)", att.Filename, (att.Size+1023)/1024))
			}
			a.Logf("Bridge auto-attaching %d local file(s) directly to Gemini: %s", len(localAtts), strings.Join(names, ", "))
			attachments = append(attachments, localAtts...)
			uploadedSigs = discoveredSigs
		}
	}

	// Spillover oversized code / tool-result prompts to a .txt file upload so Gemini gets
	// 100% verbatim content without browser input truncation or sluggish DOM paste.
	if a.Cfg.BridgeSpilloverChars > 0 && len(bridgePrompt) > a.Cfg.BridgeSpilloverChars {
		origLen := len(bridgePrompt)
		compactPrompt, spillAtt := SpilloverOversizedPrompt(bridgePrompt, a.Cfg.BridgeSpilloverChars, isDelta)
		if spillAtt != nil {
			bridgePrompt = compactPrompt
			attachments = append(attachments, *spillAtt)
			a.Logf("Bridge spilled oversized prompt (%d chars) into attachment %s (%d KB)",
				origLen, spillAtt.Filename, (spillAtt.Size+1023)/1024)
		}
	}

	return preparedBridgeTurn{
		Prompt:        bridgePrompt,
		Attachments:   attachments,
		SessionClient: sessClient,
		SessionConv:   sessConvGen,
		Fingerprints:  fingerprints,
		UploadedSigs:  uploadedSigs,
	}
}
