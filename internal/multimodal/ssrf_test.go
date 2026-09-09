package multimodal

import (
	"net/http"
	"strings"
	"testing"
)

type mockRequester struct {
	resp *http.Response
	err  error
}

func (m *mockRequester) Do(req *http.Request) (*http.Response, error) {
	return m.resp, m.err
}

func TestFetchImageBytesSSRFReject(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"file scheme", "file:///etc/passwd", true},
		{"ftp scheme", "ftp://example.com/img.png", true},
		{"javascript scheme", "javascript:alert(1)", true},
		{"http passes scheme check", "http://example.com/img.png", false},
		{"https passes scheme check", "https://example.com/img.png", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FetchImageBytes(&mockRequester{err: http.ErrServerClosed}, tt.url)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "invalid image URL scheme") {
					t.Errorf("FetchImageBytes(%q) err=%v, want scheme error", tt.url, err)
				}
			} else {
				if err != nil && strings.Contains(err.Error(), "invalid image URL scheme") {
					t.Errorf("FetchImageBytes(%q) should NOT reject, got: %v", tt.url, err)
				}
			}
		})
	}
}
