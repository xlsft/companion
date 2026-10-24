package desktop

import (
	"context"
	"errors"
	"testing"
)

// Links that are not web pages never reach the system opener.
func TestOpenURLRejects(t *testing.T) {
	for _, link := range []string{
		"", "file:///etc/passwd", "javascript:alert(1)", "smb://host/share", "https://", "/tmp/x", "ftp://example.com",
	} {
		if err := OpenURL(context.Background(), link); !errors.Is(err, ErrBadURL) {
			t.Errorf("OpenURL(%q) = %v, want ErrBadURL", link, err)
		}
	}
}
