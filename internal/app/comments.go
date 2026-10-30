package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kilo666mj/rendercase/internal/securetoken"
	"github.com/kilo666mj/rendercase/internal/store"
)

const (
	maxCommentRunes        = 10000
	maxCommentRequestBytes = 64 << 10
	maxAnchorSelectorBytes = 512
	maxAnchorPageBytes     = 512
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
	Page     string       `json:"page,omitempty"`
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
		return nil, commentInputError{"anchor must be an object with selector, page, quote, and point"}
	}
	if a.Selector == "" || len(a.Selector) > maxAnchorSelectorBytes || !utf8.ValidString(a.Selector) || hasControl(a.Selector) {
		return nil, commentInputError{fmt.Sprintf("anchor selector must be 1 to %d bytes of printable text", maxAnchorSelectorBytes)}
	}
	if len(a.Page) > maxAnchorPageBytes || !utf8.ValidString(a.Page) || hasControl(a.Page) {
		return nil, commentInputError{fmt.Sprintf("anchor page must be at most %d bytes of printable text", maxAnchorPageBytes)}
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

var (
	errCommentArtifactNotFound = errors.New("artifact not found")
	errCommentVersionNotFound  = errors.New("artifact version not found")
	errCommentThreadNotFound   = errors.New("comment thread not found")
	errCommentNotFound         = errors.New("comment not found")
	errCommentForbidden        = errors.New("not allowed to change this comment")
)

// threadQuery selects threads for listing. With AllVersions false and Version
// 0, the artifact's latest version is used.
type threadQuery struct {
	Version     int
	AllVersions bool
	Resolved    *bool
}

func notFoundAs(err, replacement error) error {
	if errors.Is(err, store.ErrNotFound) {
		return replacement
	}
	return err
}

func newCommentID(prefix string) (string, error) {
	id, err := securetoken.ID()
	if err != nil {
		return "", err
	}
	return prefix + id, nil
}

func commentAudit(iface string, details map[string]any) map[string]any {
	if iface != "" {
		details["interface"] = iface
	}
	return details
}

func (s *Server) threadForUser(ctx context.Context, user store.User, threadID string) (store.CommentThread, store.Artifact, error) {
	t, err := s.db.CommentThread(ctx, threadID)
	if err != nil {
		return store.CommentThread{}, store.Artifact{}, notFoundAs(err, errCommentThreadNotFound)
	}
	a, err := s.db.ArtifactForUser(ctx, t.ArtifactID, user.ID)
	if err != nil {
		return store.CommentThread{}, store.Artifact{}, notFoundAs(err, errCommentThreadNotFound)
	}
	return t, a, nil
}

func (s *Server) commentForUser(ctx context.Context, user store.User, commentID string) (store.Comment, store.Artifact, error) {
	c, err := s.db.Comment(ctx, commentID)
	if err != nil {
		return store.Comment{}, store.Artifact{}, notFoundAs(err, errCommentNotFound)
	}
	if c.DeletedAt != nil {
		return store.Comment{}, store.Artifact{}, errCommentNotFound
	}
	_, a, err := s.threadForUser(ctx, user, c.ThreadID)
	if errors.Is(err, errCommentThreadNotFound) {
		return store.Comment{}, store.Artifact{}, errCommentNotFound
	}
	return c, a, err
}

func (s *Server) listCommentThreads(ctx context.Context, user store.User, artifactID string, q threadQuery) ([]threadView, error) {
	if q.Version < 0 || q.Version > math.MaxInt32 {
		return nil, commentInputError{"version must be a positive integer or all"}
	}
	a, err := s.db.ArtifactForUser(ctx, artifactID, user.ID)
	if err != nil {
		return nil, notFoundAs(err, errCommentArtifactNotFound)
	}
	filter := store.ThreadFilter{Version: q.Version, Resolved: q.Resolved}
	if q.AllVersions {
		filter.Version = 0
	} else if filter.Version == 0 {
		filter.Version = a.LatestVersion
	}
	threads, err := s.db.ListCommentThreads(ctx, a.ID, filter)
	if err != nil {
		return nil, err
	}
	out := make([]threadView, 0, len(threads))
	for _, t := range threads {
		out = append(out, newThreadView(t, a, user))
	}
	return out, nil
}

func (s *Server) createCommentThread(ctx context.Context, user store.User, artifactID string, version int, rawAnchor json.RawMessage, body, iface string) (threadView, error) {
	body, err := normalizeCommentBody(body)
	if err != nil {
		return threadView{}, err
	}
	anchor, err := validateAnchor(rawAnchor)
	if err != nil {
		return threadView{}, err
	}
	if version < 0 || version > math.MaxInt32 {
		return threadView{}, commentInputError{"version must be a positive integer"}
	}
	a, err := s.db.ArtifactForUser(ctx, artifactID, user.ID)
	if err != nil {
		return threadView{}, notFoundAs(err, errCommentArtifactNotFound)
	}
	if version == 0 {
		version = a.LatestVersion
	}
	threadID, err := newCommentID("ct_")
	if err != nil {
		return threadView{}, err
	}
	commentID, err := newCommentID("c_")
	if err != nil {
		return threadView{}, err
	}
	t, err := s.db.CreateCommentThread(ctx, store.NewCommentThread{ThreadID: threadID, CommentID: commentID, ArtifactID: a.ID, UserID: user.ID, Body: body, Version: version, Anchor: anchor})
	if err != nil {
		return threadView{}, notFoundAs(err, errCommentVersionNotFound)
	}
	s.auditContext(ctx, user.ID, "", a.ID, "comment.thread.create", commentAudit(iface, map[string]any{"thread_id": threadID, "comment_id": commentID, "version": version, "anchored": anchor != nil}))
	return newThreadView(t, a, user), nil
}

func (s *Server) replyToCommentThread(ctx context.Context, user store.User, threadID, body, iface string) (commentView, error) {
	body, err := normalizeCommentBody(body)
	if err != nil {
		return commentView{}, err
	}
	t, a, err := s.threadForUser(ctx, user, threadID)
	if err != nil {
		return commentView{}, err
	}
	commentID, err := newCommentID("c_")
	if err != nil {
		return commentView{}, err
	}
	c, err := s.db.AddComment(ctx, commentID, t.ID, user.ID, body)
	if err != nil {
		return commentView{}, notFoundAs(err, errCommentThreadNotFound)
	}
	s.auditContext(ctx, user.ID, "", a.ID, "comment.create", commentAudit(iface, map[string]any{"thread_id": t.ID, "comment_id": commentID}))
	return newCommentView(c, a, user), nil
}

func (s *Server) editComment(ctx context.Context, user store.User, commentID, body, iface string) (commentView, error) {
	body, err := normalizeCommentBody(body)
	if err != nil {
		return commentView{}, err
	}
	c, a, err := s.commentForUser(ctx, user, commentID)
	if err != nil {
		return commentView{}, err
	}
	if c.Author.ID != user.ID {
		return commentView{}, errCommentForbidden
	}
	c, err = s.db.EditComment(ctx, c.ID, body)
	if err != nil {
		return commentView{}, notFoundAs(err, errCommentNotFound)
	}
	s.auditContext(ctx, user.ID, "", a.ID, "comment.edit", commentAudit(iface, map[string]any{"thread_id": c.ThreadID, "comment_id": c.ID}))
	return newCommentView(c, a, user), nil
}

func (s *Server) deleteComment(ctx context.Context, user store.User, commentID, iface string) error {
	c, a, err := s.commentForUser(ctx, user, commentID)
	if err != nil {
		return err
	}
	if c.Author.ID != user.ID && !canModerateComments(a) {
		return errCommentForbidden
	}
	if err := s.db.DeleteComment(ctx, c.ID); err != nil {
		return notFoundAs(err, errCommentNotFound)
	}
	s.auditContext(ctx, user.ID, "", a.ID, "comment.delete", commentAudit(iface, map[string]any{"thread_id": c.ThreadID, "comment_id": c.ID, "moderated": c.Author.ID != user.ID}))
	return nil
}

func (s *Server) setCommentThreadResolved(ctx context.Context, user store.User, threadID string, resolved bool, iface string) (threadView, error) {
	t, a, err := s.threadForUser(ctx, user, threadID)
	if err != nil {
		return threadView{}, err
	}
	if t.CreatedBy.ID != user.ID && !canModerateComments(a) {
		return threadView{}, errCommentForbidden
	}
	t, err = s.db.SetThreadResolved(ctx, t.ID, user.ID, resolved)
	if err != nil {
		return threadView{}, notFoundAs(err, errCommentThreadNotFound)
	}
	action := "comment.thread.reopen"
	if resolved {
		action = "comment.thread.resolve"
	}
	s.auditContext(ctx, user.ID, "", a.ID, action, commentAudit(iface, map[string]any{"thread_id": t.ID}))
	return newThreadView(t, a, user), nil
}
