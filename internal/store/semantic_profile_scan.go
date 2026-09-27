package store

import (
	"context"
	"encoding/json/v2"
	"fmt"

	"go.kenn.io/docbank/document"
)

// FirstCurrentSemanticProfile finds one current rendition profile that carries
// the selected exact embedding descriptor. Search uses it only as query
// policy; across-profile eligibility still checks each head and vector space.
func (s *Store) FirstCurrentSemanticProfile(ctx context.Context, bindingID, descriptorFingerprint string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT p.profile_fingerprint,p.canonical_profile
		FROM processing_profiles p JOIN rendition_heads h ON h.profile_fingerprint=p.profile_fingerprint
		JOIN content_versions v ON v.version_id=h.content_version_id
		JOIN nodes n ON n.id=v.node_id AND n.current_version_id=v.version_id AND n.trashed_at IS NULL
		ORDER BY p.profile_fingerprint`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var fingerprint, encoded string
		if err := rows.Scan(&fingerprint, &encoded); err != nil {
			return "", err
		}
		var profile document.ProcessingProfileV1
		if err := json.Unmarshal([]byte(encoded), &profile, json.RejectUnknownMembers(true)); err != nil {
			return "", fmt.Errorf("decoding current semantic profile: %w", err)
		}
		for _, embedding := range profile.Embeddings {
			if embedding.Name == bindingID && embedding.Descriptor.Fingerprint == descriptorFingerprint {
				return fingerprint, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", ErrNotFound
}
