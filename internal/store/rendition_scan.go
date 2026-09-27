package store

import (
	"context"
	"errors"
	"fmt"
)

type CurrentRenditionPage struct {
	Items                  []RenditionView
	NextVersionID          string
	NextProfileFingerprint string
	HasMore                bool
}

// CurrentRenditionsPage returns a bounded keyset page of active rendition
// authorities for current, live document versions. It includes no bytes.
func (s *Store) CurrentRenditionsPage(ctx context.Context, afterVersion, afterProfile string, limit int) (CurrentRenditionPage, error) {
	if limit < 1 || limit > 100 {
		return CurrentRenditionPage{}, errors.New("rendition scan page is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT h.content_version_id,h.profile_fingerprint
		FROM rendition_heads h
		JOIN content_versions v ON v.version_id=h.content_version_id
		JOIN nodes n ON n.id=v.node_id AND n.current_version_id=v.version_id AND n.trashed_at IS NULL
		WHERE h.content_version_id>? OR (h.content_version_id=? AND h.profile_fingerprint>?)
		ORDER BY h.content_version_id,h.profile_fingerprint LIMIT ?`, afterVersion, afterVersion, afterProfile, limit+1)
	if err != nil {
		return CurrentRenditionPage{}, fmt.Errorf("listing current renditions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type key struct{ version, profile string }
	keys := make([]key, 0, limit+1)
	for rows.Next() {
		var item key
		if err := rows.Scan(&item.version, &item.profile); err != nil {
			return CurrentRenditionPage{}, fmt.Errorf("reading current rendition key: %w", err)
		}
		keys = append(keys, item)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return CurrentRenditionPage{}, fmt.Errorf("reading current rendition page: %w", err)
	}
	more := len(keys) > limit
	if more {
		keys = keys[:limit]
	}
	page := CurrentRenditionPage{Items: make([]RenditionView, 0, len(keys)), HasMore: more}
	if len(keys) != 0 {
		page.NextVersionID = keys[len(keys)-1].version
		page.NextProfileFingerprint = keys[len(keys)-1].profile
	}
	for _, item := range keys {
		view, err := s.ActiveRendition(ctx, item.version, item.profile)
		if errors.Is(err, ErrNotFound) {
			// A concurrent update won after the page snapshot. The next scan
			// will inspect its current version.
			continue
		}
		if err != nil {
			return CurrentRenditionPage{}, err
		}
		page.Items = append(page.Items, view)
	}
	return page, nil
}
