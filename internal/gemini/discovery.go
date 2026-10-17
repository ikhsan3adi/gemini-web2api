package gemini

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ModelHeaderKey = "x-goog-ext-525001261-jspb"
	HeaderJspb89   = "x-goog-ext-73010989-jspb"
	HeaderJspb90   = "x-goog-ext-73010990-jspb"
	HeaderUUIDJspb = "x-goog-ext-525005358-jspb"
)

// DiscoveredModel represents a backend model discovered dynamically from Gemini's otAQ7b RPC.
type DiscoveredModel struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Display     string `json:"display"`
	Desc        string `json:"desc"`
	Number      int    `json:"number"`
	Capacity    int    `json:"capacity"`
	CapacityFld int    `json:"capacity_field"`
}

// DiscoveredModels returns a copy of currently cached discovered models.
func (c *Client) DiscoveredModels() []DiscoveredModel {
	c.modelsMu.RLock()
	defer c.modelsMu.RUnlock()
	res := make([]DiscoveredModel, len(c.discovered))
	copy(res, c.discovered)
	return res
}

// EnsureModelsDiscovered performs dynamic model discovery if cookies are configured.
// It is thread-safe and retries on failure after a 60-second cooldown.
func (c *Client) EnsureModelsDiscovered() {
	if c.Cookies == nil {
		return
	}
	info, err := c.Cookies.Load()
	if err != nil || info.Cookie == "" {
		return
	}

	c.discMu.Lock()
	defer c.discMu.Unlock()

	c.modelsMu.RLock()
	hasDiscovered := len(c.discovered) > 0
	c.modelsMu.RUnlock()
	if hasDiscovered {
		return
	}

	if !c.lastDiscFail.IsZero() && time.Since(c.lastDiscFail) < 60*time.Second {
		return
	}

	models, err := c.DiscoverModels()
	if err != nil {
		c.lastDiscFail = time.Now()
		c.Logf("Dynamic model discovery failed: %v", err)
		return
	}
	c.lastDiscFail = time.Time{}
	c.Logf("Discovered %d account models (capacity=%d)", len(models), c.accountCap)
}

// DiscoverModels queries Gemini's GetUserStatus RPC (otAQ7b) to discover account models dynamically.
func (c *Client) DiscoverModels() ([]DiscoveredModel, error) {
	if c.Cookies == nil {
		return nil, nil
	}
	cookieInfo, err := c.Cookies.Load()
	if err != nil || cookieInfo.Cookie == "" {
		return nil, nil
	}

	authUser := c.Cfg.AuthUser
	if authUser == "" && cookieInfo.AuthUser != "" {
		authUser = cookieInfo.AuthUser
	}

	xsrf := c.Cfg.XSRFToken
	if xsrf == "" && cookieInfo.XSRFToken != "" {
		xsrf = cookieInfo.XSRFToken
	}

	bl := c.CurrentBL()
	if bl == "" && cookieInfo.GeminiBL != "" {
		bl = cookieInfo.GeminiBL
	}

	prefix := AccountPrefix(authUser)
	q := url.Values{}
	q.Set("rpcids", "otAQ7b")
	q.Set("hl", "en")
	q.Set("_reqid", fmt.Sprintf("%d", time.Now().Unix()%1000000))
	q.Set("rt", "c")
	q.Set("source-path", "/app")
	if bl != "" {
		q.Set("bl", bl)
	}

	endpoint := fmt.Sprintf("https://gemini.google.com%s/_/BardChatUi/data/batchexecute?%s", prefix, q.Encode())

	freq, _ := json.Marshal([]any{[]any{[]any{"otAQ7b", "[]", nil, "generic"}}})
	form := url.Values{}
	form.Set("f.req", string(freq))
	if xsrf != "" {
		form.Set("at", xsrf)
	}

	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header = c.buildHeaders()
	batchUUID := strings.ToUpper(uuidV4())
	hdr, _ := json.Marshal([]any{1, nil, nil, nil, nil, nil, nil, nil, []int{4, 5, 6, 8}, nil, nil, nil, nil, nil, nil, nil, batchUUID})
	req.Header.Set(ModelHeaderKey, string(hdr))
	req.Header.Set(HeaderJspb89, "[0]")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("batchexecute status %d", resp.StatusCode)
	}

	partBody := findRPCBody(string(body), "otAQ7b")
	if partBody == nil {
		return nil, fmt.Errorf("otAQ7b RPC response body not found")
	}

	tier, _ := at(partBody, 16).([]any)
	capab, _ := at(partBody, 17).([]any)
	capacity, field := computeCapacity(tier, capab)

	list, _ := at(partBody, 15).([]any)
	var models []DiscoveredModel
	for _, item := range list {
		m, ok := item.([]any)
		if !ok || len(m) == 0 {
			continue
		}
		id, _ := at(m, 0).(string)
		if id == "" {
			continue
		}
		dm := DiscoveredModel{
			ID:          id,
			Category:    firstString(at(m, 1), at(m, 10)),
			Display:     firstString(at(m, 11), at(m, 19), at(m, 1)),
			Desc:        firstString(at(m, 12), at(m, 2)),
			Capacity:    capacity,
			CapacityFld: field,
			Number:      1,
		}
		if n, ok := asInt(at(m, 17)); ok {
			dm.Number = n
		} else if n, ok := asInt(at(m, 9)); ok {
			dm.Number = n
		}
		models = append(models, dm)
	}

	c.modelsMu.Lock()
	c.discovered = models
	c.accountCap = capacity
	c.accountCapFld = field
	c.modelsMu.Unlock()

	return models, nil
}

func computeCapacity(tier, capab []any) (int, int) {
	has := func(xs []any, v int) bool {
		for _, x := range xs {
			if n, ok := asInt(x); ok && n == v {
				return true
			}
		}
		return false
	}
	switch {
	case has(tier, 21):
		return 1, 13
	case has(tier, 22):
		return 2, 13
	case has(capab, 115):
		return 4, 12 // Plus accounts
	case has(tier, 16) || has(capab, 106):
		return 3, 12 // Pro accounts
	case has(tier, 8) || has(capab, 19):
		return 2, 12 // Pro accounts
	default:
		return 1, 12 // Free accounts
	}
}

func findRPCBody(raw, rpcID string) []any {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		var frames []any
		if json.Unmarshal([]byte(line), &frames) != nil {
			continue
		}
		for _, f := range frames {
			part, ok := f.([]any)
			if !ok || len(part) < 3 || at(part, 1) != rpcID {
				continue
			}
			bodyStr, ok := at(part, 2).(string)
			if !ok {
				continue
			}
			var body []any
			if json.Unmarshal([]byte(bodyStr), &body) == nil {
				return body
			}
		}
	}
	return nil
}

func at(xs []any, i int) any {
	if i < 0 || i >= len(xs) {
		return nil
	}
	return xs[i]
}

func asInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func firstString(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// resolveModelHeader builds the upstream x-goog-ext-525001261-jspb header value
// and returns the target upstream mode ID.
// Model headers are only attached when the model explicitly configures a ModelHeader
// in the registry and the session has authenticated cookies.
func (c *Client) resolveModelHeader(opt ModelOptions, reqUUID string) (string, int) {
	if opt.ModelHeader == "" || c.Cookies == nil {
		return "", opt.ModelID
	}

	cookieInfo, err := c.Cookies.Load()
	if err != nil || cookieInfo.Cookie == "" {
		return "", opt.ModelID
	}

	c.modelsMu.RLock()
	models := c.discovered
	capVal := c.accountCap
	capFld := c.accountCapFld
	c.modelsMu.RUnlock()

	var target *DiscoveredModel
	for i := range models {
		if models[i].ID == opt.ModelHeader {
			target = &models[i]
			break
		}
	}

	targetID := ""
	targetNum := opt.ModelID
	if target != nil {
		targetID = target.ID
		targetNum = target.Number
		if target.Capacity > 0 {
			capVal = target.Capacity
			capFld = target.CapacityFld
		}
	} else {
		targetID = opt.ModelHeader
		targetNum = opt.ModelID
		if capVal <= 0 {
			capVal = 2
			capFld = 12
		}
	}

	if targetID == "" {
		return "", targetNum
	}

	if capVal <= 0 {
		capVal = 1
		capFld = 12
	}

	var hdr []any
	hdr = append(hdr, 1, nil, nil, nil, targetID, nil, nil, 0, []int{4, 5, 6, 8}, nil, nil)
	if capFld == 13 {
		hdr = append(hdr, nil)
	}
	thinkingTail := 1
	if opt.Extra != nil && opt.Extra[80] == 2 {
		thinkingTail = 2
	}
	hdr = append(hdr, capVal, nil, nil, targetNum, thinkingTail, reqUUID)

	b, err := json.Marshal(hdr)
	if err != nil {
		return "", targetNum
	}
	return string(b), targetNum
}
