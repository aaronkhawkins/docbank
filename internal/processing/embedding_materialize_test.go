package processing

import (
	"context"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/retrieval"
	"go.kenn.io/docbank/internal/store"
	"go.kenn.io/docbank/internal/vectorworker"
)

func TestMaterializeRenditionQueuesExistingEvidenceWithoutReprocessingOriginal(t *testing.T) {
	fixture := newPublicationFixture(t)
	staged := fixture.stage(t, publicationIDs{"b8", "58", "98"}, strings.Repeat("synthetic registration evidence ", 8), "Example")
	publisher, err := NewArtifactPublisher(fixture.catalog, fixture.blobs)
	require.NoError(t, err)
	_, err = publisher.PublishRendition(t.Context(), staged)
	require.NoError(t, err)
	view, err := fixture.catalog.ActiveRendition(t.Context(), fixture.versionID, fixture.profile.Fingerprint)
	require.NoError(t, err)
	fake := newEmbeddingWorkerFixture(t)
	var template document.ProcessingProfileV1
	require.NoError(t, json.Unmarshal(fake.profile.CanonicalProfile, &template))
	var binding document.EmbeddingBindingV1
	for _, candidate := range template.Embeddings {
		if candidate.InputKind == document.EmbeddingInputRenditionChunk {
			binding = candidate
		}
	}
	profile, _, err := DeriveEmbeddingProfile(view, binding)
	require.NoError(t, err)
	assert.NotEqual(t, view.Attachment.Profile.Fingerprint, profile.Fingerprint)
	assert.Equal(t, view.Build.RenditionRequestFingerprint, profile.RenditionRequestFingerprint)
	assert.Equal(t, view.Build.EvidenceLexicalFingerprint, profile.EvidenceLexicalFingerprint)
	consent := store.ProviderOperationAuthorizationRequest{Principal: "operator:synthetic", Scope: "embedding:synthetic",
		ProfileFingerprint: profile.Fingerprint, DisclosureFingerprint: binding.DisclosureFingerprint,
		InputClasses: []string{string(document.EmbeddingInputRenditionChunk)}, RetainedArtifactClasses: []string{"embedding_vector_set"},
	}
	_, err = fixture.catalog.GrantConsent(t.Context(), store.ProcessingConsentGrantRequest{
		Principal: consent.Principal, Scope: consent.Scope, ProfileFingerprint: consent.ProfileFingerprint,
		DisclosureFingerprint: consent.DisclosureFingerprint, InputClasses: consent.InputClasses,
		RetainedArtifactClasses: consent.RetainedArtifactClasses,
	})
	require.NoError(t, err)
	materializer := EmbeddingMaterializer{Catalog: fixture.catalog, Blobs: fixture.blobs}
	job, err := materializer.MaterializeRendition(t.Context(), view, binding, fake.descriptor,
		embeddingIntegrationTokenizer{}, consent)
	require.NoError(t, err)
	assert.NotEmpty(t, job.ID)
	derived, err := fixture.catalog.ActiveRendition(t.Context(), fixture.versionID, profile.Fingerprint)
	require.NoError(t, err)
	assert.Equal(t, view.Build.ID, derived.Build.ID)
	assert.NotEqual(t, view.Attachment.ID, derived.Attachment.ID)

	// An unchanged scan reuses the same retained authority and job identity.
	again, err := materializer.MaterializeRendition(t.Context(), view, binding, fake.descriptor,
		embeddingIntegrationTokenizer{}, consent)
	require.NoError(t, err)
	assert.Equal(t, job.ID, again.ID)

	runtime, err := NewProviderEmbeddingRuntime(&embeddingWorkerProvider{
		runtime: fake.runtime, binding: binding.Name, descriptor: fake.descriptor,
	}, fixture.blobs, t.TempDir(), fake.runtime.Classify)
	require.NoError(t, err)
	worker, err := NewEmbeddingWorker(EmbeddingWorkerConfig{Catalog: fixture.catalog, Authority: fixture.catalog,
		Blobs: fixture.blobs, GenerationBlobs: fixture.blobs, Runtime: runtime, Gate: api.NewOperationGate(),
		Owner: "synthetic-worker", LeaseDuration: time.Minute, IdleDelay: time.Millisecond,
		RetryLimit: 3, MaxRetryDelay: time.Second, AttemptLifetime: time.Minute,
		MaxRows: 1000, MaxDimensions: 8, MaxVectorBlobBytes: 1 << 20,
		DescriptorFingerprints: []string{fake.descriptor.Fingerprint},
	})
	require.NoError(t, err)
	processed, err := worker.ScanOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	spaces, err := fixture.catalog.ListVectorIndexSpaces(t.Context())
	require.NoError(t, err)
	require.Len(t, spaces, 1)
	index, err := vectorworker.NewIndexWorker(vectorworker.IndexWorkerConfig{
		Catalog: fixture.catalog, Mutate: api.NewOperationGate().MutateContext,
		Owner: "synthetic-index", BuildLease: time.Minute, ReaderLease: time.Minute, IdleDelay: time.Millisecond,
		ReadVectorSet: func(ctx context.Context, member store.VectorIndexMember) ([]byte, error) {
			return fixture.catalog.ReadVectorIndexVectorSet(ctx, fixture.blobs, member)
		},
	})
	require.NoError(t, err)
	_, err = index.Rebuild(t.Context(), spaces[0])
	require.NoError(t, err)
	searcher, err := retrieval.NewSearcher(retrieval.SearcherConfig{
		Backend: fixture.catalog, Encoders: &embeddingWorkerProvider{
			runtime: fake.runtime, binding: "query", descriptor: fake.descriptor,
		}, Owner: "synthetic-search", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	report, err := searcher.Search(t.Context(), retrieval.Query{
		Text: "registration evidence", Mode: retrieval.ModeHybrid, Limit: 5,
		ProcessingProfileFingerprint: profile.Fingerprint, BindingID: binding.Name,
		Authorization: document.EmbeddingAuthorization{
			ProviderID: fake.descriptor.ID, DescriptorFingerprint: fake.descriptor.Fingerprint,
			PolicyFingerprint: fake.descriptor.PolicyFingerprint, MaxBatchItems: 1,
			MaxInputBytes: 1 << 20, MaxResponseBytes: 1 << 20,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, retrieval.ModeHybrid, report.ActualMode)
	require.Len(t, report.Results, 1)
	assert.Equal(t, fixture.versionID, report.Results[0].Document.ContentVersionID)
}

func TestScanCurrentRenditionsBackfillsOnce(t *testing.T) {
	fixture := newPublicationFixture(t)
	staged := fixture.stage(t, publicationIDs{"b9", "59", "99"}, "Synthetic existing document evidence", "Example")
	publisher, err := NewArtifactPublisher(fixture.catalog, fixture.blobs)
	require.NoError(t, err)
	_, err = publisher.PublishRendition(t.Context(), staged)
	require.NoError(t, err)
	fake := newEmbeddingWorkerFixture(t)
	var template document.ProcessingProfileV1
	require.NoError(t, json.Unmarshal(fake.profile.CanonicalProfile, &template))
	var binding document.EmbeddingBindingV1
	for _, candidate := range template.Embeddings {
		if candidate.InputKind == document.EmbeddingInputRenditionChunk {
			binding = candidate
		}
	}
	materializer := EmbeddingMaterializer{Catalog: fixture.catalog, Blobs: fixture.blobs}
	first, err := materializer.ScanCurrentRenditions(t.Context(), binding, fake.descriptor,
		embeddingIntegrationTokenizer{}, "operator:synthetic", "embedding:synthetic")
	require.NoError(t, err)
	assert.Equal(t, 1, first.Queued)
	second, err := materializer.ScanCurrentRenditions(t.Context(), binding, fake.descriptor,
		embeddingIntegrationTokenizer{}, "operator:synthetic", "embedding:synthetic")
	require.NoError(t, err)
	assert.Zero(t, second.Queued)
	assert.GreaterOrEqual(t, second.Skipped, 1)
}
