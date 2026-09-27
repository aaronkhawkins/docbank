package processing

import (
	"context"
	"errors"
	"fmt"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/store"
)

type EmbeddingMaterializationReport struct {
	Examined int
	Queued   int
	Skipped  int
	Failed   int
}

// ScanCurrentRenditions applies the same source-owned materialization to a
// bounded page at a time. A later scan picks up newly published renditions.
func (m EmbeddingMaterializer) ScanCurrentRenditions(ctx context.Context,
	binding document.EmbeddingBindingV1, descriptor document.EmbeddingDescriptor,
	tokenizer document.Tokenizer, principal, scope string,
) (EmbeddingMaterializationReport, error) {
	var report EmbeddingMaterializationReport
	var afterVersion, afterProfile string
	for {
		page, err := m.Catalog.CurrentRenditionsPage(ctx, afterVersion, afterProfile, 100)
		if err != nil {
			return report, err
		}
		for _, view := range page.Items {
			report.Examined++
			hasEvidence := false
			for _, artifact := range view.Build.Artifacts {
				if artifact.Role == "normalized_evidence" {
					hasEvidence = true
					break
				}
			}
			if !hasEvidence {
				report.Skipped++
				continue
			}
			profile, _, err := DeriveEmbeddingProfile(view, binding)
			if errors.Is(err, ErrEmbeddingBindingPresent) {
				report.Skipped++
				continue
			}
			if err != nil {
				report.Failed++
				continue
			}
			exists, err := m.Catalog.HasEmbeddingJob(ctx, view.Attachment.ContentVersionID, profile.Fingerprint, binding.Name)
			if err != nil {
				return report, err
			}
			if exists {
				report.Skipped++
				continue
			}
			consent := store.ProviderOperationAuthorizationRequest{
				Principal: principal, Scope: scope, ProfileFingerprint: profile.Fingerprint,
				DisclosureFingerprint: binding.DisclosureFingerprint,
				InputClasses:          []string{string(binding.InputKind)}, RetainedArtifactClasses: []string{"embedding_vector_set"},
			}
			if err := m.Catalog.EnsureInitialConsent(ctx, store.ProcessingConsentGrantRequest{
				Principal: consent.Principal, Scope: consent.Scope, ProfileFingerprint: consent.ProfileFingerprint,
				DisclosureFingerprint: consent.DisclosureFingerprint,
				InputClasses:          consent.InputClasses, RetainedArtifactClasses: consent.RetainedArtifactClasses,
			}); err != nil {
				if errors.Is(err, store.ErrProcessingConsentRevoked) {
					report.Skipped++
					continue
				}
				report.Failed++
				continue
			}
			_, err = m.MaterializeRendition(ctx, view, binding, descriptor, tokenizer, consent)
			if errors.Is(err, ErrEmbeddingEvidenceMissing) {
				report.Skipped++
				continue
			}
			if err != nil {
				report.Failed++
				continue
			}
			report.Queued++
		}
		if !page.HasMore {
			break
		}
		afterVersion, afterProfile = page.NextVersionID, page.NextProfileFingerprint
	}
	if report.Failed != 0 {
		return report, fmt.Errorf("%d rendition embeddings could not be queued", report.Failed)
	}
	return report, nil
}
