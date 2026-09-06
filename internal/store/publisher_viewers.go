package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

func publisherViewerID(ctx context.Context, tx pgx.Tx, subject string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE oidc_subject=$1`, subject).Scan(&id); err != nil {
		return "", fmt.Errorf("resolve configured publisher viewer %q (recipient must have signed in): %w", subject, err)
	}
	return id, nil
}

// BackfillPublisherViewers validates recipients and grants access to existing
// active artifacts. It preserves ownership, visibility, and existing grant roles.
// Removing a mapping does not revoke grants previously created by it.
func (d *DB) BackfillPublisherViewers(ctx context.Context, mappings map[string]string) (count int64, err error) {
	if len(mappings) == 0 {
		return 0, nil
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackTransaction(ctx, tx, &err)
	// Consistent ordering avoids deadlocks when multiple replicas start together.
	publishers := make([]string, 0, len(mappings))
	for publisher := range mappings {
		publishers = append(publishers, publisher)
	}
	sort.Strings(publishers)
	for _, publisher := range publishers {
		viewerID, lookupErr := publisherViewerID(ctx, tx, mappings[publisher])
		if lookupErr != nil {
			return 0, lookupErr
		}
		result, insertErr := tx.Exec(ctx, `INSERT INTO artifact_grants(artifact_id,user_id,role)
   SELECT a.id,$2,'viewer' FROM artifacts a JOIN users u ON u.id=a.owner_id
   WHERE u.oidc_subject=$1 AND a.deleted_at IS NULL AND a.owner_id<>$2
   ORDER BY a.id ON CONFLICT (artifact_id,user_id) DO NOTHING`, publisher, viewerID)
		if insertErr != nil {
			return 0, insertErr
		}
		count += result.RowsAffected()
	}
	return count, tx.Commit(ctx)
}
