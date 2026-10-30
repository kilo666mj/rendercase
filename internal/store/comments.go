package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const MaxCommentsPerArtifact = 2000

var ErrCommentLimit = errors.New("comment limit reached for this artifact")

type CommentAuthor struct{ ID, DisplayName string }

type Comment struct {
	ID, ThreadID        string
	Author              CommentAuthor
	Body                string
	CreatedAt           time.Time
	EditedAt, DeletedAt *time.Time
}

type CommentThread struct {
	ID, ArtifactID string
	Version        int
	Anchor         json.RawMessage
	CreatedBy      CommentAuthor
	CreatedAt      time.Time
	ResolvedAt     *time.Time
	ResolvedBy     *CommentAuthor
	Comments       []Comment
}

type NewCommentThread struct {
	ThreadID, CommentID, ArtifactID, UserID, Body string
	Version                                       int
	Anchor                                        json.RawMessage
}

// ThreadFilter selects threads. Version 0 selects every version and a nil
// Resolved selects both open and resolved threads.
type ThreadFilter struct {
	Version  int
	Resolved *bool
}

// Threads are visible only while their artifact is active and at least one
// comment in them has not been deleted.
const threadSelect = `SELECT t.id,t.artifact_id,t.version,t.anchor,t.created_by,COALESCE(NULLIF(cu.display_name,''),cu.username),
	t.created_at,t.resolved_at,t.resolved_by,COALESCE(NULLIF(ru.display_name,''),ru.username,'')
	FROM comment_threads t
	JOIN artifacts a ON a.id=t.artifact_id AND a.deleted_at IS NULL
	JOIN users cu ON cu.id=t.created_by
	LEFT JOIN users ru ON ru.id=t.resolved_by
	WHERE EXISTS (SELECT 1 FROM comments live WHERE live.thread_id=t.id AND live.deleted_at IS NULL)`

const commentSelect = `SELECT c.id,c.thread_id,c.author_id,COALESCE(NULLIF(u.display_name,''),u.username),c.body,c.created_at,c.edited_at,c.deleted_at
	FROM comments c JOIN users u ON u.id=c.author_id`

func scanThread(row pgx.Row) (CommentThread, error) {
	var t CommentThread
	var anchor []byte
	var resolvedBy *string
	var resolvedName string
	if err := row.Scan(&t.ID, &t.ArtifactID, &t.Version, &anchor, &t.CreatedBy.ID, &t.CreatedBy.DisplayName, &t.CreatedAt, &t.ResolvedAt, &resolvedBy, &resolvedName); err != nil {
		return CommentThread{}, translate(err)
	}
	if len(anchor) > 0 {
		t.Anchor = json.RawMessage(anchor)
	}
	if resolvedBy != nil {
		t.ResolvedBy = &CommentAuthor{ID: *resolvedBy, DisplayName: resolvedName}
	}
	return t, nil
}

func scanComment(row pgx.Row) (Comment, error) {
	var c Comment
	err := row.Scan(&c.ID, &c.ThreadID, &c.Author.ID, &c.Author.DisplayName, &c.Body, &c.CreatedAt, &c.EditedAt, &c.DeletedAt)
	return c, translate(err)
}

// lockCommentQuota serializes comment creation per artifact and enforces
// MaxCommentsPerArtifact.
func lockCommentQuota(ctx context.Context, tx pgx.Tx, artifactID string) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM artifacts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, artifactID).Scan(&id); err != nil {
		return translate(err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM comments c JOIN comment_threads t ON t.id=c.thread_id WHERE t.artifact_id=$1`, artifactID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxCommentsPerArtifact {
		return ErrCommentLimit
	}
	return nil
}

func (d *DB) CreateCommentThread(ctx context.Context, in NewCommentThread) (_ CommentThread, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return CommentThread{}, err
	}
	defer rollbackTransaction(ctx, tx, &err)
	if err = lockCommentQuota(ctx, tx, in.ArtifactID); err != nil {
		return CommentThread{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM artifact_versions WHERE artifact_id=$1 AND version=$2)`, in.ArtifactID, in.Version).Scan(&exists); err != nil {
		return CommentThread{}, err
	}
	if !exists {
		return CommentThread{}, ErrNotFound
	}
	if _, err = tx.Exec(ctx, `INSERT INTO comment_threads(id,artifact_id,version,anchor,created_by) VALUES($1,$2,$3,$4,$5)`,
		in.ThreadID, in.ArtifactID, in.Version, nullString(string(in.Anchor)), in.UserID); err != nil {
		return CommentThread{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO comments(id,thread_id,author_id,body,created_at) VALUES($1,$2,$3,$4,clock_timestamp())`,
		in.CommentID, in.ThreadID, in.UserID, in.Body); err != nil {
		return CommentThread{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CommentThread{}, err
	}
	return d.CommentThread(ctx, in.ThreadID)
}

func (d *DB) AddComment(ctx context.Context, commentID, threadID, userID, body string) (_ Comment, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return Comment{}, err
	}
	defer rollbackTransaction(ctx, tx, &err)
	var artifactID string
	// A thread with no live comment is hidden, so it cannot be revived by a reply.
	if err = tx.QueryRow(ctx, `SELECT t.artifact_id FROM comment_threads t WHERE t.id=$1
		AND EXISTS (SELECT 1 FROM comments c WHERE c.thread_id=t.id AND c.deleted_at IS NULL)`, threadID).Scan(&artifactID); err != nil {
		return Comment{}, translate(err)
	}
	if err = lockCommentQuota(ctx, tx, artifactID); err != nil {
		return Comment{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO comments(id,thread_id,author_id,body,created_at) VALUES($1,$2,$3,$4,clock_timestamp())`,
		commentID, threadID, userID, body); err != nil {
		return Comment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Comment{}, err
	}
	return d.Comment(ctx, commentID)
}

func (d *DB) CommentThread(ctx context.Context, threadID string) (CommentThread, error) {
	t, err := scanThread(d.Pool.QueryRow(ctx, threadSelect+` AND t.id=$1`, threadID))
	if err != nil {
		return CommentThread{}, err
	}
	threads := []CommentThread{t}
	if err := d.loadComments(ctx, threads); err != nil {
		return CommentThread{}, err
	}
	return threads[0], nil
}

func (d *DB) ListCommentThreads(ctx context.Context, artifactID string, f ThreadFilter) ([]CommentThread, error) {
	rows, err := d.Pool.Query(ctx, threadSelect+` AND t.artifact_id=$1 AND ($2::integer=0 OR t.version=$2)
		AND ($3::boolean IS NULL OR (t.resolved_at IS NOT NULL)=$3) ORDER BY t.created_at,t.id`, artifactID, f.Version, f.Resolved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	threads := []CommentThread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return threads, d.loadComments(ctx, threads)
}

func (d *DB) loadComments(ctx context.Context, threads []CommentThread) error {
	if len(threads) == 0 {
		return nil
	}
	ids := make([]string, len(threads))
	index := make(map[string]int, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
		index[t.ID] = i
		threads[i].Comments = []Comment{}
	}
	rows, err := d.Pool.Query(ctx, commentSelect+` WHERE c.thread_id=ANY($1) ORDER BY c.created_at,c.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return err
		}
		i := index[c.ThreadID]
		threads[i].Comments = append(threads[i].Comments, c)
	}
	return rows.Err()
}

func (d *DB) Comment(ctx context.Context, commentID string) (Comment, error) {
	return scanComment(d.Pool.QueryRow(ctx, commentSelect+`
		JOIN comment_threads t ON t.id=c.thread_id
		JOIN artifacts a ON a.id=t.artifact_id AND a.deleted_at IS NULL
		WHERE c.id=$1`, commentID))
}

// visibleComment matches a live comment whose thread and artifact are visible.
const visibleComment = `c.deleted_at IS NULL AND EXISTS (SELECT 1 FROM comment_threads t
	JOIN artifacts a ON a.id=t.artifact_id AND a.deleted_at IS NULL WHERE t.id=c.thread_id)`

func (d *DB) EditComment(ctx context.Context, commentID, body string) (Comment, error) {
	ct, err := d.Pool.Exec(ctx, `UPDATE comments c SET body=$2,edited_at=now() WHERE c.id=$1 AND `+visibleComment, commentID, body)
	if err != nil {
		return Comment{}, err
	}
	if ct.RowsAffected() != 1 {
		return Comment{}, ErrNotFound
	}
	return d.Comment(ctx, commentID)
}

func (d *DB) DeleteComment(ctx context.Context, commentID string) error {
	ct, err := d.Pool.Exec(ctx, `UPDATE comments c SET body='',deleted_at=now() WHERE c.id=$1 AND `+visibleComment, commentID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// SetThreadResolved changes only threads that are visible: the artifact is
// active and at least one comment is live. RowsAffected counts matched rows,
// so resolving an already-resolved visible thread still succeeds.
func (d *DB) SetThreadResolved(ctx context.Context, threadID, userID string, resolved bool) (CommentThread, error) {
	const visible = `WHERE t.id=$1 AND EXISTS (SELECT 1 FROM artifacts a WHERE a.id=t.artifact_id AND a.deleted_at IS NULL)
		AND EXISTS (SELECT 1 FROM comments c WHERE c.thread_id=t.id AND c.deleted_at IS NULL)`
	var ct pgconn.CommandTag
	var err error
	if resolved {
		ct, err = d.Pool.Exec(ctx, `UPDATE comment_threads t SET resolved_at=COALESCE(t.resolved_at,now()),resolved_by=COALESCE(t.resolved_by,$2) `+visible, threadID, userID)
	} else {
		ct, err = d.Pool.Exec(ctx, `UPDATE comment_threads t SET resolved_at=NULL,resolved_by=NULL `+visible, threadID)
	}
	if err != nil {
		return CommentThread{}, err
	}
	if ct.RowsAffected() != 1 {
		return CommentThread{}, ErrNotFound
	}
	return d.CommentThread(ctx, threadID)
}
