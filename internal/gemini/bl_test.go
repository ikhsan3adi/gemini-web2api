package gemini

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

type stubRequester struct {
	lastReq *http.Request
	body    string
	err     error
}

func (s *stubRequester) Do(req *http.Request) (*http.Response, error) {
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
	}, nil
}

func TestFetchLatestBLUsesRequester(t *testing.T) {
	stub := &stubRequester{body: `x boq_assistant-bard-web-server_20260716.08_p0 y`}
	got, err := FetchLatestBL(stub)
	if err != nil {
		t.Fatalf("FetchLatestBL error: %v", err)
	}
	if got != "boq_assistant-bard-web-server_20260716.08_p0" {
		t.Errorf("FetchLatestBL = %q, want BL", got)
	}
	if stub.lastReq == nil || stub.lastReq.URL.Host != "gemini.google.com" {
		t.Errorf("FetchLatestBL did not use the provided Requester, req=%v", stub.lastReq)
	}
	if _, ok := stub.lastReq.Context().Deadline(); !ok {
		t.Error("FetchLatestBL request should carry a timeout deadline")
	}
}

func TestBLRegex(t *testing.T) {
	tests := []struct {
		name  string
		html  string
		want  string
		found bool
	}{
		{
			name:  "found",
			html:  `some garbage boq_assistant-bard-web-server_20260716.08_p0 more garbage`,
			want:  "boq_assistant-bard-web-server_20260716.08_p0",
			found: true,
		},
		{
			name:  "not found",
			html:  `no matching pattern here`,
			want:  "",
			found: false,
		},
		{
			name:  "multiple matches returns first",
			html:  `boq_assistant-bard-web-server_20260101.01_p1 and boq_assistant-bard-web-server_20260716.08_p0`,
			want:  "boq_assistant-bard-web-server_20260101.01_p1",
			found: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reBL.FindString(tt.html)
			if tt.found {
				if got != tt.want {
					t.Errorf("reBL.FindString() = %q, want %q", got, tt.want)
				}
			} else {
				if got != "" {
					t.Errorf("reBL.FindString() = %q, want empty", got)
				}
			}
		})
	}
}

func TestCurrentBL(t *testing.T) {
	c := &Client{
		bl: "mutable-bl",
		Cfg: config.Config{
			GeminiBL: "config-bl",
		},
	}
	if got := c.CurrentBL(); got != "mutable-bl" {
		t.Errorf("CurrentBL() = %q, want %q", got, "mutable-bl")
	}

	c2 := &Client{
		Cfg: config.Config{
			GeminiBL: "config-bl",
		},
	}
	if got := c2.CurrentBL(); got != "config-bl" {
		t.Errorf("CurrentBL() empty bl = %q, want %q", got, "config-bl")
	}
}

func TestSetBL(t *testing.T) {
	c := &Client{
		Cfg: config.Config{
			GeminiBL: "old",
		},
	}
	c.SetBL("new")
	if got := c.CurrentBL(); got != "new" {
		t.Errorf("After SetBL, CurrentBL() = %q, want %q", got, "new")
	}
}
