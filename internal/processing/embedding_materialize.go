package processing

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/store"
)

// EmbeddingMaterializer reuses a verified rendition build and its canonical
// evidence. It creates no OCR output and never embeds text inside SQLite.
type EmbeddingMaterializer struct {
	Catalog *store.Store
	Blobs   *blob.Store
}

var (
	ErrEmbeddingBindingPresent  = errors.New("embedding binding already belongs to rendition profile")
	ErrEmbeddingEvidenceMissing = errors.New("bounded normalized evidence is unavailable")
)

// MaterializeRendition queues one current rendition for an existing embedding
// worker. Consent must already have been granted for the derived profile.
func (m EmbeddingMaterializer) MaterializeRendition(ctx context.Context, view store.RenditionView,
	binding document.EmbeddingBindingV1, descriptor document.EmbeddingDescriptor,
	tokenizer document.Tokenizer, consent store.ProviderOperationAuthorizationRequest,
) (store.EmbeddingJob, error) {
	if m.Catalog == nil || m.Blobs == nil {
		return store.EmbeddingJob{}, errors.New("embedding materializer requires catalog and blobs")
	}
	if binding.InputKind != document.EmbeddingInputRenditionChunk || binding.Chunk == nil {
		return store.EmbeddingJob{}, errors.New("embedding materializer requires a rendition chunk binding")
	}
	profile, fingerprints, err := DeriveEmbeddingProfile(view, binding)
	if err != nil {
		return store.EmbeddingJob{}, err
	}
	if consent.ProfileFingerprint != profile.Fingerprint || consent.DisclosureFingerprint != binding.DisclosureFingerprint {
		return store.EmbeddingJob{}, errors.New("embedding consent does not match derived profile")
	}
	var evidenceArtifact *store.RenditionArtifactRecord
	for i := range view.Build.Artifacts {
		if view.Build.Artifacts[i].Role == "normalized_evidence" {
			evidenceArtifact = &view.Build.Artifacts[i]
			break
		}
	}
	if evidenceArtifact == nil || evidenceArtifact.Size < 1 || evidenceArtifact.Size > 64<<20 {
		return store.EmbeddingJob{}, ErrEmbeddingEvidenceMissing
	}
	reader, err := m.Blobs.OpenContext(ctx, evidenceArtifact.BlobHash)
	if err != nil {
		return store.EmbeddingJob{}, errors.New("normalized evidence could not be opened")
	}
	evidenceBytes, readErr := io.ReadAll(io.LimitReader(reader, evidenceArtifact.Size+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || int64(len(evidenceBytes)) != evidenceArtifact.Size ||
		workerHashBytes(evidenceBytes) != evidenceArtifact.BlobHash {
		return store.EmbeddingJob{}, errors.New("normalized evidence failed verification")
	}
	var evidence document.NormalizedEvidenceV1
	if err := json.Unmarshal(evidenceBytes, &evidence, json.RejectUnknownMembers(true)); err != nil {
		return store.EmbeddingJob{}, errors.New("normalized evidence is invalid")
	}
	evidence.Checksum = evidenceArtifact.BlobHash
	policy, err := document.NewInputPolicy(binding, tokenizer, fingerprints.EvidenceLexical, nil)
	if err != nil {
		return store.EmbeddingJob{}, err
	}
	generation, err := document.BuildEmbeddingInputs(evidence, policy, document.GenerationLimits{
		MaxInputs: 1000, MaxTotalContentTokens: 1_000_000, MaxTotalRenderedTokens: 1_000_000,
		MaxTotalContentBytes: 64 << 20, MaxTotalRenderedBytes: 64 << 20,
		MaxFittingWorkTokens: 10_000_000, MaxFittingWorkBytes: 1 << 30,
	})
	if err != nil {
		return store.EmbeddingJob{}, fmt.Errorf("building embedding inputs: %w", err)
	}
	generationBytes, err := document.MarshalEmbeddingInputGeneration(generation)
	if err != nil {
		return store.EmbeddingJob{}, err
	}
	receipt, err := m.Blobs.WriteDetailedContext(ctx, bytes.NewReader(generationBytes))
	if err != nil {
		return store.EmbeddingJob{}, errors.New("embedding input could not be retained")
	}
	encoding, err := receipt.EncodingName()
	if err != nil {
		return store.EmbeddingJob{}, err
	}
	if err := m.Catalog.RecordRenditionBlob(ctx, receipt.Hash, receipt.Size, store.BlobPhysical{
		Encoding: encoding, StoredBytes: receipt.StoredSize, PackEligible: receipt.PackEligible, Created: receipt.Created,
	}); err != nil {
		return store.EmbeddingJob{}, err
	}
	now := view.Attachment.AttachedAt
	attachment := view.Attachment
	attachment.ID = workerHashString("embedding-attachment/v1\x00" + view.Attachment.ID + "\x00" + profile.Fingerprint)
	attachment.Profile = profile
	attachment.AttachedAt = now
	lexical, err := m.Catalog.ActiveLexicalGeneration(ctx)
	if err != nil {
		return store.EmbeddingJob{}, err
	}
	if err := m.Catalog.PublishRenditionAndLexicalHeads(ctx, attachment, store.RenditionHeadRecord{
		ContentVersionID: attachment.ContentVersionID, ProcessingProfileFingerprint: profile.Fingerprint,
		AttachmentID: attachment.ID, PublishedAt: now,
	}, lexical.ID); err != nil {
		return store.EmbeddingJob{}, err
	}
	input := store.EmbeddingInputGenerationRecord{
		ID:              workerHashString("embedding-generation-attachment/v1\x00" + generation.Checksum + "\x00" + attachment.ID),
		SourceVersionID: attachment.ContentVersionID, ProcessingProfileFingerprint: profile.Fingerprint,
		GenerationJSON: generationBytes, EvidenceJSON: evidenceBytes,
		GenerationBlobHash: receipt.Hash, GenerationEncodedSize: receipt.Size,
		GenerationChecksum: generation.Checksum, EvidenceFingerprint: generation.EvidenceChecksum,
		TokenizerFingerprint:   workerHashString(binding.Chunk.Tokenizer + "\x00" + binding.Chunk.TokenizerRevision),
		ChunkPolicyFingerprint: generation.PolicyFingerprint,
		FormatterFingerprint:   workerHashString(binding.Chunk.Formatter), AttachmentID: attachment.ID,
		CreatedAt: now,
	}
	for _, generated := range generation.Inputs {
		input.Inputs = append(input.Inputs, store.EmbeddingInputReference{ID: generated.Key, RenderedChecksum: generated.Checksum})
	}
	return m.Catalog.EnqueueEmbeddingJob(ctx, store.EmbeddingJobRequest{
		ContentVersionID: attachment.ContentVersionID, Profile: profile, BindingID: binding.Name,
		Descriptor: descriptor, InputGeneration: input, Authorization: consent,
	})
}

// DeriveEmbeddingProfile extends only the embedding binding of an existing
// rendition profile. Its rendition and lexical identities must still match
// the verified build, so a caller can grant exact-profile consent beforehand.
func DeriveEmbeddingProfile(view store.RenditionView, binding document.EmbeddingBindingV1) (store.ProcessingProfileRecord, document.FingerprintSet, error) {
	var portable document.ProcessingProfileV1
	if err := json.Unmarshal(view.Attachment.Profile.CanonicalProfile, &portable, json.RejectUnknownMembers(true)); err != nil {
		return store.ProcessingProfileRecord{}, document.FingerprintSet{}, fmt.Errorf("reading rendition profile: %w", err)
	}
	for _, existing := range portable.Embeddings {
		if existing.Name == binding.Name {
			return store.ProcessingProfileRecord{}, document.FingerprintSet{}, ErrEmbeddingBindingPresent
		}
	}
	portable.Embeddings = append(portable.Embeddings, binding)
	profileBytes, fingerprints, err := document.CanonicalProfile(portable)
	if err != nil {
		return store.ProcessingProfileRecord{}, document.FingerprintSet{}, fmt.Errorf("deriving embedding profile: %w", err)
	}
	profile := view.Attachment.Profile
	profile.CanonicalProfile = profileBytes
	profile.Fingerprint = fingerprints.Profile
	profile.RenditionRequestFingerprint = fingerprints.RenditionRequest
	profile.EvidenceLexicalFingerprint = fingerprints.EvidenceLexical
	profile.RetentionDisclosureFingerprint = fingerprints.RetentionDisclosure
	if profile.RenditionRequestFingerprint != view.Build.RenditionRequestFingerprint ||
		profile.EvidenceLexicalFingerprint != view.Build.EvidenceLexicalFingerprint {
		return store.ProcessingProfileRecord{}, document.FingerprintSet{}, errors.New("embedding profile changes verified rendition authority")
	}
	return profile, fingerprints, nil
}
