// Package suppliedocr publishes caller-produced focr output through Docbank's
// existing verified rendition artifact and lexical-head machinery.
package suppliedocr

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/store"
)

// Publisher owns no independent storage path; it is bound to the daemon's
// existing catalog and blob stores.
type Publisher struct {
	catalog *store.Store
	blobs   *blob.Store
}

func New(catalog *store.Store, blobs *blob.Store) (*Publisher, error) {
	if catalog == nil || blobs == nil {
		return nil, errors.New("supplied OCR publisher requires catalog and blob stores")
	}
	return &Publisher{catalog: catalog, blobs: blobs}, nil
}

func (p *Publisher) PublishSuppliedOCR(
	ctx context.Context, nodeID int64, metadata api.SuppliedOCRPublicationMetadata,
	transcript, structured []byte,
) (api.SuppliedOCRPublicationReceipt, error) {
	input := processing.SuppliedOCRPublicationInput{
		VaultID: p.catalog.VaultID(), ContentVersionID: metadata.ContentVersionID,
		SubmissionKey: metadata.SubmissionKey,
		Result: document.SuppliedOCRResult{
			Family: metadata.Family, Engine: metadata.Engine, EngineVersion: metadata.EngineVersion,
			Model: metadata.Model, Recipe: metadata.Recipe,
			ManifestSHA256: metadata.ManifestSHA256, ManifestBytes: metadata.ManifestBytes,
			ProducedAt: metadata.ProducedAt, SourceSHA256: metadata.SourceSHA256,
			Text: string(transcript), Structured: structured,
		},
	}
	candidate, err := processing.BuildSuppliedOCRPublicationCandidate(input)
	if err != nil {
		return api.SuppliedOCRPublicationReceipt{}, api.NewError(
			http.StatusUnprocessableEntity, "validation", err.Error())
	}
	existing, existingErr := p.catalog.RenditionBuildByID(ctx, candidate.Staged.Build.ID)
	if existingErr == nil && (!bytes.Equal(existing.ProviderReceipt, candidate.Staged.Build.ProviderReceipt) ||
		existing.ProviderOperationID != metadata.SubmissionKey) {
		return api.SuppliedOCRPublicationReceipt{}, api.NewError(
			http.StatusConflict, "submission_conflict",
			"submission key already names different supplied OCR material")
	}
	if existingErr != nil && !errors.Is(existingErr, store.ErrNotFound) {
		return api.SuppliedOCRPublicationReceipt{}, existingErr
	}
	if existingErr == nil && existing.RenditionRequestFingerprint != candidate.Staged.Build.RenditionRequestFingerprint {
		// An exact legacy submission keeps its immutable request/profile identity.
		// The material and submission key were already verified above.
		input.LegacyRequestIdentity = true
		legacy, legacyErr := processing.BuildSuppliedOCRPublicationCandidate(input)
		if legacyErr != nil {
			return api.SuppliedOCRPublicationReceipt{}, legacyErr
		}
		if existing.RenditionRequestFingerprint == legacy.Staged.Build.RenditionRequestFingerprint {
			candidate = legacy
		}
	}
	wasExact := false
	active, activeErr := p.catalog.ActiveRendition(ctx, metadata.ContentVersionID,
		candidate.Staged.Attachment.Profile.Fingerprint)
	if activeErr == nil {
		wasExact = active.Build.ID == candidate.Staged.Build.ID &&
			active.Attachment.ID == candidate.Staged.Attachment.ID
	} else if !errors.Is(activeErr, store.ErrNotFound) {
		return api.SuppliedOCRPublicationReceipt{}, activeErr
	}
	publisher, err := processing.NewArtifactPublisher(p.catalog, p.blobs)
	if err != nil {
		return api.SuppliedOCRPublicationReceipt{}, err
	}
	published, err := publisher.PublishRendition(ctx, candidate.Staged)
	if err != nil {
		if errors.Is(err, store.ErrRenditionBuildConflict) {
			return api.SuppliedOCRPublicationReceipt{}, api.NewError(
				http.StatusConflict, "submission_conflict",
				"submission key already names different supplied OCR material")
		}
		return api.SuppliedOCRPublicationReceipt{}, err
	}
	status := "added"
	if wasExact {
		status = "skipped"
	}
	return api.SuppliedOCRPublicationReceipt{
		Status: status, NodeID: nodeID, ContentVersionID: metadata.ContentVersionID,
		SourceSHA256: metadata.SourceSHA256, SubmissionKey: metadata.SubmissionKey,
		MaterialChecksum: candidate.MaterialChecksum, BuildID: published.BuildID,
		AttachmentID: published.AttachmentID, LexicalGenerationID: published.LexicalGeneration.ID,
		EvidenceChecksum: candidate.Staged.Build.EvidenceChecksum,
	}, nil
}
