package store

import (
	"context"
	"fmt"
)

// HasEmbeddingJob reports whether the current version/profile/binding already
// has retained work. Failed work remains visible for explicit repair.
func (s *Store) HasEmbeddingJob(ctx context.Context, versionID, profileFingerprint, bindingID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM embedding_jobs
		WHERE content_version_id=? AND profile_fingerprint=? AND binding_id=?)`,
		versionID, profileFingerprint, bindingID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking retained embedding work: %w", err)
	}
	return exists, nil
}
