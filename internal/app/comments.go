package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kilo666mj/rendercase/internal/store"
)

const (
	maxCommentRunes        = 10000
	maxCommentRequestBytes = 64 << 10
	maxAnchorSelectorBytes = 512
	maxAnchorQuoteRunes    = 500
	maxAnchorContextRunes  = 32
)

// commentInputError marks client mistakes; its message is safe to return.
type commentInputError struct{ msg string }

func (e commentInputError) Error() string { return e.msg }

func normalizeCommentBody(v string) (string, error) {
	if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
		return "", commentInputError{"comment must be valid UTF-8 text without NUL characters"}
	}
	v = strings.TrimSpace(v)
	n := utf8.RuneCountInString(v)
	if n == 0 {
		return "", commentInputError{"comment must not be empty"}
	}
	if n > maxCommentRunes {
		return "", commentInputError{fmt.Sprintf("comment exceeds %d characters", maxCommentRunes)}
	}
	return v, nil
}

type commentAnchor struct {
	Selector string       `json:"selector"`
	Quote    *anchorQuote `json:"quote,omitempty"`
	Point    *anchorPoint `json:"point,omitempty"`
}

type anchorQuote struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

type anchorPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func hasControl(v string) bool {
	return strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// validateAnchor checks an untrusted anchor from the browser and returns its
// normalized encoding, or nil for a comment on the whole version.
func validateAnchor(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var a commentAnchor
	if err := dec.Decode(&a); err != nil || dec.More() {
		return nil, commentInputError{"anchor must be an object with selector, quote, and point"}
	}
	if a.Selector == "" || len(a.Selector) > maxAnchorSelectorBytes || !utf8.ValidString(a.Selector) || hasControl(a.Selector) {
		return nil, commentInputError{fmt.Sprintf("anchor selector must be 1 to %d bytes of printable text", maxAnchorSelectorBytes)}
	}
	if q := a.Quote; q != nil {
		if q.Exact == "" || utf8.RuneCountInString(q.Exact) > maxAnchorQuoteRunes {
			return nil, commentInputError{fmt.Sprintf("anchor quote must be 1 to %d characters", maxAnchorQuoteRunes)}
		}
		if utf8.RuneCountInString(q.Prefix) > maxAnchorContextRunes || utf8.RuneCountInString(q.Suffix) > maxAnchorContextRunes {
			return nil, commentInputError{fmt.Sprintf("anchor quote context must be at most %d characters", maxAnchorContextRunes)}
		}
		if strings.ContainsRune(q.Exact+q.Prefix+q.Suffix, 0) {
			return nil, commentInputError{"anchor quote must not contain NUL characters"}
		}
	}
	if p := a.Point; p != nil && (p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1) {
		return nil, commentInputError{"anchor point coordinates must be between 0 and 1"}
	}
	return json.Marshal(a)
}

type commentAuthorView struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type commentView struct {
	ID        string            `json:"id"`
	Author    commentAuthorView `json:"author"`
	Body      string            `json:"body"`
	CreatedAt time.Time         `json:"created_at"`
	EditedAt  *time.Time        `json:"edited_at,omitempty"`
	Deleted   bool              `json:"deleted"`
	CanEdit   bool              `json:"can_edit"`
	CanDelete bool              `json:"can_delete"`
}

type threadView struct {
	ID         string             `json:"id"`
	Version    int                `json:"version"`
	Anchor     *commentAnchor     `json:"anchor,omitempty"`
	CreatedBy  commentAuthorView  `json:"created_by"`
	CreatedAt  time.Time          `json:"created_at"`
	ResolvedAt *time.Time         `json:"resolved_at,omitempty"`
	ResolvedBy *commentAuthorView `json:"resolved_by,omitempty"`
	CanResolve bool               `json:"can_resolve"`
	Comments   []commentView      `json:"comments"`
}

func canModerateComments(a store.Artifact) bool { return a.Role == "owner" || a.Role == "editor" }

func authorView(a store.CommentAuthor) commentAuthorView {
	return commentAuthorView{ID: a.ID, DisplayName: a.DisplayName}
}

func newCommentView(c store.Comment, a store.Artifact, u store.User) commentView {
	deleted := c.DeletedAt != nil
	own := c.Author.ID == u.ID
	return commentView{
		ID: c.ID, Author: authorView(c.Author), Body: c.Body, CreatedAt: c.CreatedAt, EditedAt: c.EditedAt,
		Deleted: deleted, CanEdit: !deleted && own, CanDelete: !deleted && (own || canModerateComments(a)),
	}
}

func newThreadView(t store.CommentThread, a store.Artifact, u store.User) threadView {
	v := threadView{
		ID: t.ID, Version: t.Version, CreatedBy: authorView(t.CreatedBy), CreatedAt: t.CreatedAt, ResolvedAt: t.ResolvedAt,
		CanResolve: t.CreatedBy.ID == u.ID || canModerateComments(a), Comments: make([]commentView, 0, len(t.Comments)),
	}
	if len(t.Anchor) > 0 {
		var anchor commentAnchor
		// Stored anchors were validated on write; a decode failure only hides the pin.
		if json.Unmarshal(t.Anchor, &anchor) == nil {
			v.Anchor = &anchor
		}
	}
	if t.ResolvedBy != nil {
		by := authorView(*t.ResolvedBy)
		v.ResolvedBy = &by
	}
	for _, c := range t.Comments {
		v.Comments = append(v.Comments, newCommentView(c, a, u))
	}
	return v
}
