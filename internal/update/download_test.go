package update

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDownloaderFetch(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 1000)
	tests := []struct {
		name    string
		partial int    // bytes already in the .part file
		range_  bool   // the server honours Range
		serve   []byte // what the server sends for a full request
		err     error
	}{
		{"fresh", 0, true, body, nil},
		{"resume", 4000, true, body, nil},
		{"server ignores range", 4000, false, body, nil},
		{"short body", 0, true, body[:5000], ErrOffline},
		{"oversized body", 0, true, append(append([]byte{}, body...), 'x'), ErrOffline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRange string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRange = r.Header.Get("Range")
				if tt.range_ && gotRange != "" {
					from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(gotRange, "bytes="), "-"))
					w.Header().Set("Content-Range", "bytes "+strconv.Itoa(from)+"-"+strconv.Itoa(len(body)-1)+"/"+strconv.Itoa(len(body)))
					w.WriteHeader(http.StatusPartialContent)
					w.Write(tt.serve[from:])
					return
				}
				w.Write(tt.serve)
			}))
			defer srv.Close()
			dst := filepath.Join(t.TempDir(), "a.tar.gz")
			if tt.partial > 0 {
				if err := os.WriteFile(dst+".part", body[:tt.partial], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var last int64
			d := &Downloader{Client: srv.Client()}
			err := d.Fetch(context.Background(), Asset{Name: "a.tar.gz", Size: int64(len(body)), DownloadURL: srv.URL}, dst, func(done, total int64) {
				last = done
				if total != int64(len(body)) {
					t.Errorf("total = %d", total)
				}
			})
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				if _, err := os.Stat(dst); err == nil {
					t.Error("incomplete download was renamed into place")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.partial > 0 && gotRange != "bytes="+strconv.Itoa(tt.partial)+"-" {
				t.Errorf("Range = %q", gotRange)
			}
			got, _ := os.ReadFile(dst)
			if !bytes.Equal(got, body) || last != int64(len(body)) {
				t.Errorf("file %d bytes, progress %d", len(got), last)
			}
		})
	}
}

func TestDownloaderCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1000))
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	d := &Downloader{Client: srv.Client()}
	err := d.Fetch(ctx, Asset{Size: 1 << 20, DownloadURL: srv.URL}, filepath.Join(t.TempDir(), "x"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
