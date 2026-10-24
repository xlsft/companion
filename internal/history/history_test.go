package history

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/pkg/contracts"
)

func newRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	data := filepath.Join(dir, "data")
	return New(db, data), data
}

func capMeta(id string) contracts.CaptureMetadata {
	return contracts.CaptureMetadata{
		ID: contracts.CaptureID(id), DisplayID: "d1",
		Native: contracts.PixelSize{W: 20, H: 10}, Model: contracts.PixelSize{W: 10, H: 5},
		Bounds: contracts.RectXYWH(0, 0, 10, 5), Scale: 2, Rotation: 90, TopologyVersion: 7,
		Timestamp: time.Unix(1700000000, 0),
	}
}

func imgs(ids ...string) map[contracts.CaptureID]*image.RGBA {
	m := map[contracts.CaptureID]*image.RGBA{}
	for _, id := range ids {
		m[contracts.CaptureID(id)] = image.NewRGBA(image.Rect(0, 0, 4, 2))
	}
	return m
}

func mustConv(t *testing.T, r *Repo) *Conversation {
	t.Helper()
	c, err := r.CreateConversation(context.Background(), "t", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustAppend(t *testing.T, r *Repo, conv string, text string, caps []contracts.CaptureMetadata, im map[contracts.CaptureID]*image.RGBA) string {
	t.Helper()
	id, err := r.AppendMessage(context.Background(), conv, Message{Role: RoleUser, Text: text}, caps, im)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestAppendGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, _ := newRepo(t)
	c, _ := r.CreateConversation(ctx, "hello", "agent-1")
	_, err := r.AppendMessage(ctx, c.ID, Message{
		SessionID: "s1", Generation: 3, Role: RoleUser, Text: "what is this", Mode: ModeTalk,
		ProviderID: "p", Model: "m", Usage: []byte(`{"in":5}`),
		Attachments: []Attachment{{Kind: AttachURL, Name: "x", SourceURL: "https://e.com"}},
	}, []contracts.CaptureMetadata{capMeta("c1")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, r, c.ID, "answer", nil, nil)

	got, err := r.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "hello" || got.AgentID != "agent-1" || got.KeepScreenshots || len(got.Messages) != 2 {
		t.Fatalf("conv = %+v", got)
	}
	m := got.Messages[0]
	if m.Text != "what is this" || m.SessionID != "s1" || m.Generation != 3 || m.Mode != ModeTalk || string(m.Usage) != `{"in":5}` {
		t.Errorf("msg = %+v", m)
	}
	if len(m.Captures) != 1 || len(m.Attachments) != 1 || m.Attachments[0].SourceURL != "https://e.com" {
		t.Fatalf("children = %+v", m)
	}
	cp := m.Captures[0]
	want := capMeta("c1")
	if cp.ID != want.ID || cp.Native != want.Native || cp.Model != want.Model || cp.Bounds != want.Bounds ||
		cp.Scale != 2 || cp.Rotation != 90 || cp.TopologyVersion != 7 || !cp.TakenAt.Equal(want.Timestamp) || cp.BlobPath != "" {
		t.Errorf("capture = %+v", cp)
	}
	if got.Messages[1].Mode != ModeText {
		t.Errorf("default mode = %q", got.Messages[1].Mode)
	}
	list, err := r.List(ctx, Page{})
	if err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Errorf("list = %+v %v", list, err)
	}
	if _, err := r.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}
	if _, err := r.AppendMessage(ctx, "nope", Message{Role: RoleUser}, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("append missing conv: %v", err)
	}
	if _, err := r.AppendMessage(ctx, c.ID, Message{Role: "bogus"}, nil, nil); err == nil {
		t.Error("invalid role accepted")
	}
}

func TestScreenshotsNotWrittenByDefault(t *testing.T) {
	r, data := newRepo(t)
	c := mustConv(t, r)
	mustAppend(t, r, c.ID, "x", []contracts.CaptureMetadata{capMeta("c1")}, imgs("c1"))
	if n := countFiles(t, data); n != 0 {
		t.Fatalf("%d files written without opt-in", n)
	}
	got, _ := r.Get(context.Background(), c.ID)
	if len(got.Messages[0].Captures) != 1 || got.Messages[0].Captures[0].BlobPath != "" {
		t.Errorf("metadata not kept or blob set: %+v", got.Messages[0].Captures)
	}
}

func TestOptInWritesAndOptOutDeletes(t *testing.T) {
	ctx := context.Background()
	r, data := newRepo(t)
	c := mustConv(t, r)
	if err := r.SetKeepScreenshots(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, r, c.ID, "x", []contracts.CaptureMetadata{capMeta("c1"), capMeta("c2")}, imgs("c1"))
	got, _ := r.Get(ctx, c.ID)
	caps := got.Messages[0].Captures
	if !got.KeepScreenshots || caps[0].BlobPath == "" || caps[1].BlobPath != "" {
		t.Fatalf("caps = %+v", caps)
	}
	f, err := os.Open(caps[0].BlobPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(f)
	f.Close()
	if err != nil || format != "png" || cfg.Width != 4 {
		t.Errorf("decode = %v %q %v", cfg, format, err)
	}
	if want := filepath.Join(data, "captures", c.ID, "c1.png"); caps[0].BlobPath != want {
		t.Errorf("path = %s want %s", caps[0].BlobPath, want)
	}

	if err := r.SetKeepScreenshots(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, data); n != 0 {
		t.Errorf("%d files left after opt-out", n)
	}
	got, _ = r.Get(ctx, c.ID)
	if got.KeepScreenshots || len(got.Messages[0].Captures) != 2 || got.Messages[0].Captures[0].BlobPath != "" {
		t.Errorf("after opt-out: %+v", got)
	}
	if err := r.SetKeepScreenshots(ctx, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing conv: %v", err)
	}
}

func TestSearchCyrillicAndEnglish(t *testing.T) {
	ctx := context.Background()
	r, _ := newRepo(t)
	a, b, c := mustConv(t, r), mustConv(t, r), mustConv(t, r)
	mustAppend(t, r, a.ID, "Открой настройки звука, пожалуйста", nil, nil)
	mustAppend(t, r, b.ID, "Please open the Settings window", nil, nil)
	mustAppend(t, r, b.ID, "Second settings mention", nil, nil)
	mustAppend(t, r, c.ID, "unrelated", nil, nil)

	tests := []struct {
		q    string
		want []string
	}{
		{"настройки", []string{a.ID}},
		{"НАСТРОЙКИ", []string{a.ID}}, // case-insensitive for Cyrillic
		{"настр", []string{a.ID}},     // prefix
		{"settings", []string{b.ID}},  // one hit per conversation
		{"open settings", []string{b.ID}},
		{`"; DROP TABLE messages; --`, nil}, // never parsed as FTS syntax
		{"", nil},
		{"zzz", nil},
	}
	for _, tt := range tests {
		hits, err := r.Search(ctx, tt.q, 10)
		if err != nil {
			t.Errorf("%q: %v", tt.q, err)
			continue
		}
		var got []string
		for _, h := range hits {
			got = append(got, h.ConversationID)
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%q: got %v want %v", tt.q, got, tt.want)
		}
	}
	hits, _ := r.Search(ctx, "настройки", 10)
	if !strings.Contains(hits[0].Snippet, "[Настройки]") && !strings.Contains(hits[0].Snippet, "[настройки]") {
		t.Errorf("snippet = %q", hits[0].Snippet)
	}
	if hits, _ := r.Search(ctx, "settings", 1); len(hits) != 1 {
		t.Errorf("limit ignored: %d", len(hits))
	}
	// Deleting a conversation removes it from the index.
	if err := r.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := r.Search(ctx, "настройки", 10); len(hits) != 0 {
		t.Errorf("deleted conv still searchable: %v", hits)
	}
}

func rowCount(t *testing.T, r *Repo, table string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteCascadesRowsAndFiles(t *testing.T) {
	ctx := context.Background()
	r, data := newRepo(t)
	a, b := mustConv(t, r), mustConv(t, r)
	for _, c := range []*Conversation{a, b} {
		if err := r.SetKeepScreenshots(ctx, c.ID, true); err != nil {
			t.Fatal(err)
		}
		mustAppend(t, r, c.ID, "x", []contracts.CaptureMetadata{capMeta("c1")}, imgs("c1"))
	}
	if countFiles(t, data) != 2 {
		t.Fatal("setup")
	}
	if err := r.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if countFiles(t, data) != 1 || rowCount(t, r, "messages") != 1 || rowCount(t, r, "captures") != 1 {
		t.Errorf("after delete: files=%d msgs=%d caps=%d", countFiles(t, data), rowCount(t, r, "messages"), rowCount(t, r, "captures"))
	}
	if _, err := os.Stat(filepath.Join(data, "captures", a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dir remains: %v", err)
	}
	if err := r.Delete(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := r.Get(ctx, b.ID); err != nil {
		t.Errorf("other conv lost: %v", err)
	}
}

func TestDeleteAll(t *testing.T) {
	ctx := context.Background()
	r, data := newRepo(t)
	for i := 0; i < 3; i++ {
		c := mustConv(t, r)
		_ = r.SetKeepScreenshots(ctx, c.ID, true)
		mustAppend(t, r, c.ID, "hello", []contracts.CaptureMetadata{capMeta("c1")}, imgs("c1"))
	}
	if err := r.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"conversations", "messages", "captures", "attachments"} {
		if n := rowCount(t, r, tbl); n != 0 {
			t.Errorf("%s: %d rows", tbl, n)
		}
	}
	if n := countFiles(t, data); n != 0 {
		t.Errorf("%d files left", n)
	}
	if hits, _ := r.Search(ctx, "hello", 10); len(hits) != 0 {
		t.Errorf("index not cleared: %v", hits)
	}
	if err := r.DeleteAll(ctx); err != nil { // idempotent on empty
		t.Errorf("empty delete all: %v", err)
	}
}

func TestGCRemovesOrphans(t *testing.T) {
	ctx := context.Background()
	r, data := newRepo(t)
	if n, err := r.GC(ctx); err != nil || n != 0 {
		t.Fatalf("gc without dir: %d %v", n, err)
	}
	c := mustConv(t, r)
	_ = r.SetKeepScreenshots(ctx, c.ID, true)
	mustAppend(t, r, c.ID, "x", []contracts.CaptureMetadata{capMeta("c1")}, imgs("c1"))

	orphanDir := filepath.Join(data, "captures", "deadbeef")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(orphanDir, "a.png"), []byte("x"), 0o600)
	strayFile := filepath.Join(data, "captures", c.ID, "stray.png")
	_ = os.WriteFile(strayFile, []byte("x"), 0o600)

	n, err := r.GC(ctx)
	if err != nil || n != 2 {
		t.Fatalf("gc = %d %v", n, err)
	}
	if _, err := os.Stat(orphanDir); !errors.Is(err, os.ErrNotExist) {
		t.Error("orphan dir kept")
	}
	if _, err := os.Stat(strayFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("stray file kept")
	}
	got, _ := r.Get(ctx, c.ID)
	if _, err := os.Stat(got.Messages[0].Captures[0].BlobPath); err != nil {
		t.Errorf("referenced blob removed: %v", err)
	}
}

func TestAppendFailureLeavesNoFiles(t *testing.T) {
	ctx := context.Background()
	r, data := newRepo(t)
	c := mustConv(t, r)
	_ = r.SetKeepScreenshots(ctx, c.ID, true)
	// Duplicate capture id in one message violates the primary key after the
	// first image was already written.
	_, err := r.AppendMessage(ctx, c.ID, Message{Role: RoleUser, Text: "x"},
		[]contracts.CaptureMetadata{capMeta("c1"), capMeta("c1")}, imgs("c1"))
	if err == nil {
		t.Fatal("expected error")
	}
	if n := countFiles(t, data); n != 0 || rowCount(t, r, "messages") != 0 {
		t.Errorf("leftovers: files=%d msgs=%d", n, rowCount(t, r, "messages"))
	}
}
