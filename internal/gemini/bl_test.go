package gemini

import (
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

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
