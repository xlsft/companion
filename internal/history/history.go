// Package history stores local conversations in SQLite (FR-DATA-001).
//
// Message text is never logged here. Screenshot pixels live on disk under
// <dataDir>/captures/<conversation>/ and only when the conversation opted in
// (FR-SCREEN-006); rows are always deleted before their files, so a failed
// file removal can leave an orphan file (reaped by GC) but never a row that
// points at a missing file.
package history

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlsft/openclick/internal/store"
	"github.com/xlsft/openclick/pkg/contracts"
)

// ErrNotFound is returned when a conversation does not exist.
var ErrNotFound = store.ErrNotFound

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

type Mode string

const (
	ModeTalk      Mode = "talk"
	ModeText      Mode = "text"
	ModeDictation Mode = "dictation"
)

type AttachmentKind string

const (
	AttachFile  AttachmentKind = "file"
	AttachImage AttachmentKind = "image"
	AttachURL   AttachmentKind = "url"
)

// Conversation is a thread of messages. Messages and captures are only
// populated by Get.
type Conversation struct {
	ID              string
	Title           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	KeepScreenshots bool
	AgentID         string
	Messages        []Message
}

// Message is one turn. ID and CreatedAt are filled on append when empty.
type Message struct {
	ID         string
	SessionID  contracts.SessionID
	Generation uint64
	Role       Role
	Text       string
	Mode       Mode
	CreatedAt  time.Time
	ProviderID string
	Model      string
	// Usage is provider token accounting as JSON; nil is stored as {}.
	Usage json.RawMessage
	// Attachments are metadata only; the blob (if any) is owned by the caller.
	Attachments []Attachment
	// Captures is filled by Get with the metadata of screens sent along.
	Captures []Capture
}

// Capture is the persisted subset of contracts.CaptureMetadata. BlobPath is
// absolute and empty when no image was kept.
type Capture struct {
	ID              contracts.CaptureID
	DisplayID       contracts.DisplayID
	Bounds          contracts.Rect
	Native, Model   contracts.PixelSize
	Scale           float64
	Rotation        int
	TopologyVersion uint64
	TakenAt         time.Time
	BlobPath        string
}

type Attachment struct {
	ID        string
	Kind      AttachmentKind
	Name      string
	MediaType string
	Size      int64
	// BlobPath is relative to the data dir; empty when nothing is stored.
	BlobPath  string
	SourceURL string
}

// Page selects a window of the conversation list.
type Page struct{ Limit, Offset int }

// SearchHit is one conversation matching a query, with a snippet of the best
// matching message. Matches are wrapped in [ ].
type SearchHit struct {
	ConversationID string
	MessageID      string
	Snippet        string
}

// Repo is the history store.
type Repo struct {
	db      *store.DB
	dataDir string
}

// New returns a Repo whose blobs live under dataDir.
func New(db *store.DB, dataDir string) *Repo { return &Repo{db: db, dataDir: dataDir} }

const (
	tsLayout        = "2006-01-02T15:04:05.000000000Z" // fixed width, sorts lexically
	capturesDirName = "captures"
	defaultPage     = 50
	maxPage         = 200
)

func fmtTime(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(tsLayout, s)
	return t
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("history: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// safeName keeps ids usable as file names on every OS.
func safeName(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			out[i] = '_'
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}

func (r *Repo) capturesRoot() string { return filepath.Join(r.dataDir, capturesDirName) }

// abs resolves a stored relative blob path, refusing anything that escapes
// the data dir (a corrupted row must not make us delete foreign files).
func (r *Repo) abs(rel string) (string, error) {
	clean := path.Clean(filepath.ToSlash(rel))
	if rel == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("history: unsafe blob path")
	}
	return filepath.Join(r.dataDir, filepath.FromSlash(clean)), nil
}

// CreateConversation inserts an empty conversation. Screenshots are off.
func (r *Repo) CreateConversation(ctx context.Context, title, agentID string) (*Conversation, error) {
	ts := time.Now().UTC()
	c := &Conversation{ID: newID(), Title: title, CreatedAt: ts, UpdatedAt: ts, AgentID: agentID}
	var agent any
	if agentID != "" {
		agent = agentID
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO conversations(id, title, created_at, updated_at, agent_id)
		VALUES (?, ?, ?, ?, ?)`, c.ID, title, fmtTime(ts), fmtTime(ts), agent)
	if err != nil {
		return nil, fmt.Errorf("history: create conversation: %w", err)
	}
	return c, nil
}

// AppendMessage stores m and its capture metadata. Images are written to
// disk only if the conversation has keep_screenshots on; otherwise only
// metadata is stored. It returns the stored message id.
func (r *Repo) AppendMessage(ctx context.Context, convID string, m Message, captures []contracts.CaptureMetadata, imgs map[contracts.CaptureID]*image.RGBA) (string, error) {
	switch m.Role {
	case RoleUser, RoleAssistant, RoleTool, RoleSystem:
	default:
		return "", fmt.Errorf("history: invalid role %q", m.Role)
	}
	if m.Mode == "" {
		m.Mode = ModeText
	}
	if m.ID == "" {
		m.ID = newID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	usage := "{}"
	if len(m.Usage) > 0 {
		usage = string(m.Usage)
	}

	var written []string // absolute paths created by this call, removed on failure
	cleanup := func() {
		for _, p := range written {
			_ = os.Remove(p)
		}
	}

	err := r.db.Tx(ctx, func(tx *sql.Tx) error {
		var keep bool
		if err := tx.QueryRowContext(ctx, `SELECT keep_screenshots FROM conversations WHERE id = ?`, convID).Scan(&keep); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages
			(id, conversation_id, session_id, generation, role, text, mode, created_at, provider_id, model, usage)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, convID, string(m.SessionID), m.Generation, string(m.Role), m.Text, string(m.Mode),
			fmtTime(m.CreatedAt), m.ProviderID, m.Model, usage); err != nil {
			return err
		}
		for _, c := range captures {
			bounds, err := json.Marshal(c.Bounds)
			if err != nil {
				return err
			}
			var blob any
			if img := imgs[c.ID]; keep && img != nil {
				rel, abs, err := r.reserveBlob(convID, c.ID, m.ID)
				if err != nil {
					return err
				}
				if err := writePNG(abs, img); err != nil {
					return err
				}
				written = append(written, abs)
				blob = rel
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO captures
				(id, message_id, display_id, bounds, native_w, native_h, model_w, model_h, scale, rotation, topology_version, taken_at, blob_path)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				string(c.ID), m.ID, string(c.DisplayID), string(bounds), c.Native.W, c.Native.H, c.Model.W, c.Model.H,
				c.Scale, c.Rotation, c.TopologyVersion, fmtTime(c.Timestamp), blob); err != nil {
				return err
			}
		}
		for _, a := range m.Attachments {
			id := a.ID
			if id == "" {
				id = newID()
			}
			var blob, src any
			if a.BlobPath != "" {
				blob = a.BlobPath
			}
			if a.SourceURL != "" {
				src = a.SourceURL
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO attachments
				(id, message_id, kind, name, media_type, size, blob_path, source_url) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, m.ID, string(a.Kind), a.Name, a.MediaType, a.Size, blob, src); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE conversations SET updated_at = ? WHERE id = ?`, fmtTime(time.Now()), convID)
		return err
	})
	if err != nil {
		cleanup()
		if errors.Is(err, ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("history: append message: %w", err)
	}
	return m.ID, nil
}

// reserveBlob picks a file name under captures/<conv>/ that is not taken yet
// (capture ids repeat across process restarts).
func (r *Repo) reserveBlob(convID string, id contracts.CaptureID, msgID string) (rel, abs string, err error) {
	base := safeName(string(id))
	rel = path.Join(capturesDirName, safeName(convID), base+".png")
	abs, err = r.abs(rel)
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		rel = path.Join(capturesDirName, safeName(convID), base+"-"+safeName(msgID)+".png")
		abs, err = r.abs(rel)
	}
	return rel, abs, err
}

func writePNG(abs string, img *image.RGBA) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	tmp := abs + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, abs)
}

// List returns conversations, most recently updated first.
func (r *Repo) List(ctx context.Context, p Page) ([]Conversation, error) {
	if p.Limit <= 0 {
		p.Limit = defaultPage
	}
	if p.Limit > maxPage {
		p.Limit = maxPage
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, title, created_at, updated_at, keep_screenshots, COALESCE(agent_id, '')
		FROM conversations ORDER BY updated_at DESC, id LIMIT ? OFFSET ?`, p.Limit, p.Offset)
	if err != nil {
		return nil, fmt.Errorf("history: list: %w", err)
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("history: list: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanConversation(s scanner) (Conversation, error) {
	var c Conversation
	var created, updated string
	if err := s.Scan(&c.ID, &c.Title, &created, &updated, &c.KeepScreenshots, &c.AgentID); err != nil {
		return c, err
	}
	c.CreatedAt, c.UpdatedAt = parseTime(created), parseTime(updated)
	return c, nil
}

// Get returns a conversation with messages (oldest first), each with its
// captures and attachments.
func (r *Repo) Get(ctx context.Context, id string) (*Conversation, error) {
	c, err := scanConversation(r.db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at, keep_screenshots, COALESCE(agent_id, '')
		FROM conversations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("history: get: %w", err)
	}

	byID := map[string]int{}
	rows, err := r.db.QueryContext(ctx, `SELECT id, session_id, generation, role, text, mode, created_at, provider_id, model, usage
		FROM messages WHERE conversation_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, fmt.Errorf("history: get messages: %w", err)
	}
	for rows.Next() {
		var m Message
		var sid, role, mode, created, usage string
		if err := rows.Scan(&m.ID, &sid, &m.Generation, &role, &m.Text, &mode, &created, &m.ProviderID, &m.Model, &usage); err != nil {
			rows.Close()
			return nil, fmt.Errorf("history: get messages: %w", err)
		}
		m.SessionID, m.Role, m.Mode = contracts.SessionID(sid), Role(role), Mode(mode)
		m.CreatedAt, m.Usage = parseTime(created), json.RawMessage(usage)
		byID[m.ID] = len(c.Messages)
		c.Messages = append(c.Messages, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	crows, err := r.db.QueryContext(ctx, `SELECT c.message_id, c.id, c.display_id, c.bounds, c.native_w, c.native_h, c.model_w, c.model_h,
		c.scale, c.rotation, c.topology_version, c.taken_at, COALESCE(c.blob_path, '')
		FROM captures c JOIN messages m ON m.id = c.message_id WHERE m.conversation_id = ? ORDER BY m.rowid, c.rowid`, id)
	if err != nil {
		return nil, fmt.Errorf("history: get captures: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var mid, cid, did, bounds, taken, blob string
		var cp Capture
		if err := crows.Scan(&mid, &cid, &did, &bounds, &cp.Native.W, &cp.Native.H, &cp.Model.W, &cp.Model.H,
			&cp.Scale, &cp.Rotation, &cp.TopologyVersion, &taken, &blob); err != nil {
			return nil, fmt.Errorf("history: get captures: %w", err)
		}
		if err := json.Unmarshal([]byte(bounds), &cp.Bounds); err != nil {
			return nil, fmt.Errorf("history: capture bounds: %w", err)
		}
		cp.ID, cp.DisplayID, cp.TakenAt = contracts.CaptureID(cid), contracts.DisplayID(did), parseTime(taken)
		if blob != "" {
			if cp.BlobPath, err = r.abs(blob); err != nil {
				return nil, err
			}
		}
		i := byID[mid]
		c.Messages[i].Captures = append(c.Messages[i].Captures, cp)
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}

	arows, err := r.db.QueryContext(ctx, `SELECT a.message_id, a.id, a.kind, a.name, a.media_type, a.size, COALESCE(a.blob_path, ''), COALESCE(a.source_url, '')
		FROM attachments a JOIN messages m ON m.id = a.message_id WHERE m.conversation_id = ? ORDER BY m.rowid, a.rowid`, id)
	if err != nil {
		return nil, fmt.Errorf("history: get attachments: %w", err)
	}
	defer arows.Close()
	for arows.Next() {
		var mid, kind string
		var a Attachment
		if err := arows.Scan(&mid, &a.ID, &kind, &a.Name, &a.MediaType, &a.Size, &a.BlobPath, &a.SourceURL); err != nil {
			return nil, fmt.Errorf("history: get attachments: %w", err)
		}
		a.Kind = AttachmentKind(kind)
		i := byID[mid]
		c.Messages[i].Attachments = append(c.Messages[i].Attachments, a)
	}
	return &c, arows.Err()
}

// ftsQuery turns free text into an AND of quoted prefix terms so user input
// can never be parsed as FTS5 syntax.
func ftsQuery(q string) string {
	var terms []string
	for _, f := range strings.FieldsFunc(q, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127)
	}) {
		terms = append(terms, `"`+f+`"*`)
	}
	return strings.Join(terms, " ")
}

// Search finds conversations whose messages match query (case-insensitive,
// prefix match per word, all words required), best match first, one hit per
// conversation.
func (r *Repo) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 || limit > maxPage {
		limit = defaultPage
	}
	rows, err := r.db.QueryContext(ctx, `SELECT m.conversation_id, m.id, snippet(messages_fts, 0, '[', ']', '…', 12)
		FROM messages_fts JOIN messages m ON m.rowid = messages_fts.rowid
		WHERE messages_fts MATCH ? ORDER BY rank`, match)
	if err != nil {
		return nil, fmt.Errorf("history: search: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []SearchHit
	for rows.Next() && len(out) < limit {
		var h SearchHit
		if err := rows.Scan(&h.ConversationID, &h.MessageID, &h.Snippet); err != nil {
			return nil, fmt.Errorf("history: search: %w", err)
		}
		if seen[h.ConversationID] {
			continue
		}
		seen[h.ConversationID] = true
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetKeepScreenshots toggles the per-conversation opt-in. Turning it off
// forgets and deletes the images already stored for the conversation;
// capture metadata stays.
func (r *Repo) SetKeepScreenshots(ctx context.Context, id string, keep bool) error {
	var blobs []string
	err := r.db.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE conversations SET keep_screenshots = ? WHERE id = ?`, keep, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if keep {
			return nil
		}
		blobs, err = collectBlobs(ctx, tx, `SELECT c.blob_path FROM captures c JOIN messages m ON m.id = c.message_id
			WHERE m.conversation_id = ? AND c.blob_path IS NOT NULL`, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE captures SET blob_path = NULL WHERE message_id IN
			(SELECT id FROM messages WHERE conversation_id = ?)`, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("history: set keep screenshots: %w", err)
	}
	return errors.Join(r.removeBlobs(blobs), r.removeDirIfEmpty(id))
}

func collectBlobs(ctx context.Context, q store.Querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repo) removeBlobs(rels []string) error {
	var errs []error
	for _, rel := range rels {
		abs, err := r.abs(rel)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("history: remove blob: %w", err))
		}
	}
	return errors.Join(errs...)
}

// removeDirIfEmpty is best effort: a non-empty dir just means other captures
// of the conversation are still kept.
func (r *Repo) removeDirIfEmpty(convID string) error {
	_ = os.Remove(filepath.Join(r.capturesRoot(), safeName(convID)))
	return nil
}

// Delete removes one conversation (rows in a transaction, then its blob
// files). A file error is returned but the rows are already gone.
func (r *Repo) Delete(ctx context.Context, id string) error {
	var blobs []string
	err := r.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		blobs, err = collectBlobs(ctx, tx, `SELECT c.blob_path FROM captures c JOIN messages m ON m.id = c.message_id
			WHERE m.conversation_id = ? AND c.blob_path IS NOT NULL
			UNION SELECT a.blob_path FROM attachments a JOIN messages m ON m.id = a.message_id
			WHERE m.conversation_id = ? AND a.blob_path IS NOT NULL`, id, id)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("history: delete: %w", err)
	}
	return errors.Join(r.removeBlobs(blobs), r.removeAll(filepath.Join(r.capturesRoot(), safeName(id))))
}

// DeleteAll wipes every conversation and every stored blob. Works offline.
func (r *Repo) DeleteAll(ctx context.Context) error {
	var blobs []string
	err := r.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		blobs, err = collectBlobs(ctx, tx, `SELECT blob_path FROM captures WHERE blob_path IS NOT NULL
			UNION SELECT blob_path FROM attachments WHERE blob_path IS NOT NULL`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM conversations`)
		return err
	})
	if err != nil {
		return fmt.Errorf("history: delete all: %w", err)
	}
	return errors.Join(r.removeBlobs(blobs), r.removeAll(r.capturesRoot()))
}

func (r *Repo) removeAll(p string) error {
	if err := os.RemoveAll(p); err != nil {
		return fmt.Errorf("history: remove files: %w", err)
	}
	return nil
}

// GC removes blob directories of conversations that no longer exist and
// files inside live directories that no row references (leftovers of a
// crash or a failed file removal). It returns how many paths it removed.
func (r *Repo) GC(ctx context.Context) (int, error) {
	root := r.capturesRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("history: gc: %w", err)
	}
	live := map[string]bool{}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM conversations`)
	if err != nil {
		return 0, fmt.Errorf("history: gc: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		live[safeName(id)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	refs := map[string]bool{}
	blobs, err := collectBlobs(ctx, r.db, `SELECT blob_path FROM captures WHERE blob_path IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("history: gc: %w", err)
	}
	for _, b := range blobs {
		refs[path.Clean(filepath.ToSlash(b))] = true
	}

	removed := 0
	var errs []error
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		p := filepath.Join(root, e.Name())
		if !e.IsDir() || !live[e.Name()] {
			if err := os.RemoveAll(p); err != nil {
				errs = append(errs, err)
			} else {
				removed++
			}
			continue
		}
		files, err := os.ReadDir(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, f := range files {
			rel := path.Join(capturesDirName, e.Name(), f.Name())
			if refs[rel] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(p, f.Name())); err != nil {
				errs = append(errs, err)
			} else {
				removed++
			}
		}
	}
	return removed, errors.Join(errs...)
}
