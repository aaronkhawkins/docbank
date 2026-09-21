package retrieval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/embedding"
	"go.kenn.io/docbank/internal/store"
	"go.kenn.io/docbank/internal/vectorindex"
)

type Backend interface {
	VaultID() string
	SearchExplainedLexicalCandidates(ctx context.Context, query string, limit, offset int,
		options store.SearchOptions) ([]store.ExplainedLexicalCandidate, bool, error)
}

type FilterBackend interface {
	SearchPageWithOptions(ctx context.Context, query string, limit, offset int,
		options store.SearchOptions) ([]store.SearchHit, bool, error)
}

type SemanticBackend interface {
	AcquireSemanticSearchAuthority(ctx context.Context, profile, binding, owner string, at time.Time,
		duration time.Duration, options store.SearchOptions) (store.SemanticSearchAuthority, error)
	ResolveSemanticCandidates(ctx context.Context, profile, binding string, inputKind document.EmbeddingInputKind,
		vectorSpace, sourceManifest string, neighbors []vectorindex.Neighbor, limit int,
		options store.SearchOptions) (store.SemanticSearchResolution, error)
	ReleaseVectorIndexGeneration(ctx context.Context, leaseID string, fencingToken int64, at time.Time) error
}

type QueryEncoderResolver interface {
	ResolveQueryEncoder(ctx context.Context, descriptor document.EmbeddingDescriptor) (document.EmbeddingProvider, error)
}

type SearcherConfig struct {
	Backend       Backend
	Encoders      QueryEncoderResolver
	Owner         string
	LeaseDuration time.Duration
	Clock         func() time.Time
}

type Searcher struct {
	backend       Backend
	encoders      QueryEncoderResolver
	owner         string
	leaseDuration time.Duration
	clock         func() time.Time
}

func NewSearcher(config SearcherConfig) (*Searcher, error) {
	if config.Backend == nil {
		return nil, errors.New("retrieval backend is required")
	}
	if config.Owner == "" {
		return nil, errors.New("retrieval owner is required")
	}
	if config.LeaseDuration <= 0 {
		return nil, errors.New("retrieval vector lease duration must be positive")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Searcher{backend: config.Backend, encoders: config.Encoders, owner: config.Owner,
		leaseDuration: config.LeaseDuration, clock: config.Clock}, nil
}

func (searcher *Searcher) Search(ctx context.Context, query Query) (Report, error) {
	requested := query.Mode
	if requested == "" {
		requested = ModeAuto
	}
	query, err := normalizeQuery(query)
	if err != nil {
		return Report{}, err
	}
	switch query.Mode {
	case ModeLexical, ModeAuto:
		return searcher.lexical(ctx, query, requested, Coverage{State: CoverageUnknown})
	case ModeSemantic:
		semantic, coverage, _, truncated, err := searcher.semantic(ctx, query)
		if err != nil {
			return Report{}, err
		}
		semantic, pageTruncated := pageSlice(semantic, query.Offset, query.Limit)
		report := laneReport(requested, ModeSemantic, coverage, semantic,
			pageTruncated || (truncated && candidateWindow(query.Offset, query.Limit) < MaxCandidateLimit),
			query.Limit, query.Offset)
		if truncated && candidateWindow(query.Offset, query.Limit) == MaxCandidateLimit {
			report.SkippedReasons = []SkippedReason{SkippedSemanticCandidateLimit}
		}
		return report, nil
	case ModeHybrid:
		return searcher.hybrid(ctx, query, requested)
	}
	return Report{}, errors.New("unreachable retrieval mode")
}

var ErrSemanticUnavailable = errors.New("semantic retrieval is unavailable")

type semanticUnavailableError struct {
	reason FallbackReason
	cause  error
}

func (failure *semanticUnavailableError) Error() string {
	return fmt.Sprintf("%s: %v", ErrSemanticUnavailable, failure.cause)
}

func (failure *semanticUnavailableError) Unwrap() []error {
	return []error{ErrSemanticUnavailable, failure.cause}
}

type semanticReleaseError struct {
	operation error
	release   error
}

func (failure *semanticReleaseError) Error() string {
	return fmt.Sprintf("%v; releasing semantic generation: %v", failure.operation, failure.release)
}

func (failure *semanticReleaseError) Unwrap() []error {
	return []error{failure.operation, failure.release}
}

func (searcher *Searcher) hybrid(ctx context.Context, query Query, requested Mode) (Report, error) {
	semantic, coverage, policy, semanticTruncated, err := searcher.semantic(ctx, query)
	if err != nil {
		unavailable, unavailableOK := errors.AsType[*semanticUnavailableError](err)
		_, releaseFailed := errors.AsType[*semanticReleaseError](err)
		if unavailableOK && !releaseFailed {
			report, lexicalErr := searcher.lexical(ctx, query, requested, Coverage{State: CoverageUnknown})
			if lexicalErr != nil {
				return Report{}, lexicalErr
			}
			report.Fallback = Fallback{Applied: true, Reason: unavailable.reason}
			return report, nil
		}
		return Report{}, err
	}
	lexicalQuery := query
	lexicalQuery.Limit = policy.LexicalLimit
	lexicalQuery.Offset = 0
	lexical, lexicalTruncated, err := searcher.collectLexical(ctx, lexicalQuery)
	if err != nil {
		return Report{}, err
	}
	return makeHybridReport(requested, coverage, lexical, semantic,
		lexicalTruncated, semanticTruncated, query)
}

func (searcher *Searcher) semantic(ctx context.Context, query Query) (
	_ []Candidate, coverage Coverage, policy document.RetrievalPolicyV1, truncated bool, retErr error,
) {
	backend, ok := searcher.backend.(SemanticBackend)
	if !ok || searcher.encoders == nil {
		return nil, Coverage{State: CoverageUnknown}, document.RetrievalPolicyV1{}, false,
			&semanticUnavailableError{reason: FallbackSemanticNotConfigured,
				cause: errors.New("semantic retrieval is not configured")}
	}
	authority, err := backend.AcquireSemanticSearchAuthority(ctx,
		query.ProcessingProfileFingerprint, query.BindingID, searcher.owner,
		searcher.clock().UTC(), searcher.leaseDuration, query.Scope)
	if err != nil {
		if errors.Is(err, store.ErrSemanticAuthorityUnavailable) {
			err = &semanticUnavailableError{reason: FallbackSemanticAuthorityUnavailable, cause: err}
		}
		return nil, Coverage{State: CoverageUnknown}, document.RetrievalPolicyV1{}, false, err
	}
	policy = authority.Retrieval
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if releaseErr := backend.ReleaseVectorIndexGeneration(cleanupCtx, authority.Lease.ID,
			authority.Lease.FencingToken, searcher.clock().UTC()); releaseErr != nil {
			if retErr == nil {
				retErr = releaseErr
			} else {
				retErr = &semanticReleaseError{operation: retErr, release: releaseErr}
			}
		}
	}()
	coverage = Coverage{BindingRequired: authority.BindingRequired,
		ScopedDocuments: authority.ScopedDocuments, CompleteDocuments: authority.CompleteDocuments,
		State: CoverageComplete}
	if authority.CompleteDocuments != authority.ScopedDocuments {
		coverage.State = CoverageIncomplete
	}
	provider, err := searcher.encoders.ResolveQueryEncoder(ctx, authority.VectorSpace.Descriptor)
	if err != nil {
		return nil, coverage, policy, false,
			&semanticUnavailableError{reason: FallbackQueryEncoderUnavailable, cause: err}
	}
	if provider == nil {
		return nil, coverage, policy, false, &semanticUnavailableError{
			reason: FallbackQueryEncoderUnavailable,
			cause:  errors.New("query encoder runtime is unavailable"),
		}
	}
	if !reflect.DeepEqual(provider.Descriptor(), authority.VectorSpace.Descriptor) {
		return nil, coverage, policy, false, errors.New("query encoder does not reproduce the active vector-space descriptor")
	}
	if err := document.ValidateEmbeddingQueryCompatibility(authority.VectorSpace.Descriptor,
		provider.Descriptor()); err != nil {
		return nil, coverage, policy, false, err
	}
	inputs := []document.EmbeddingInput{{Key: "query", Role: document.EmbeddingRoleQuery,
		Kind: document.EmbeddingInputQueryText, Text: query.Text}}
	if err := document.ValidateEmbeddingProviderRequest(provider, inputs, query.Authorization); err != nil {
		return nil, coverage, policy, false, err
	}
	embedded, err := document.ExecuteEmbedding(ctx, provider, inputs, query.Authorization)
	if err != nil {
		return nil, coverage, policy, false, err
	}
	stored := authority.Lease.Generation
	generation, err := vectorindex.OpenGeneration(bytes.NewReader(stored.Bytes), int64(len(stored.Bytes)))
	if err != nil {
		return nil, coverage, policy, false, err
	}
	metadata := generation.Metadata()
	descriptor := authority.VectorSpace.Descriptor
	if metadata.VectorSpaceID != authority.VectorSpace.ID || metadata.Dimension != descriptor.Dimension ||
		metadata.Metric != descriptor.Metric || metadata.Normalization != descriptor.Normalization ||
		metadata.Manifest.Checksum != stored.IndexManifestChecksum || metadata.RowCount != stored.RowCount {
		return nil, coverage, policy, false, errors.New("leased vector generation is incompatible with active query authority")
	}
	var semanticLimit int
	if query.Mode == ModeHybrid {
		semanticLimit = policy.VectorLimit
	} else {
		semanticLimit = candidateWindow(query.Offset, query.Limit)
	}
	rawLimit := min(metadata.RowCount, MaxCandidateLimit)
	neighbors, err := generation.Search(embedded.Vectors[0].Values, rawLimit)
	if err != nil {
		return nil, coverage, policy, false, err
	}
	rawTruncated := metadata.RowCount > rawLimit
	resolution, err := backend.ResolveSemanticCandidates(ctx, query.ProcessingProfileFingerprint,
		query.BindingID, authority.InputKind, authority.VectorSpace.ID, stored.SourceManifestChecksum,
		neighbors, semanticLimit, query.Scope)
	if err != nil {
		return nil, coverage, policy, false, err
	}
	if resolution.SourceManifestChecksum != stored.SourceManifestChecksum {
		return nil, coverage, policy, false, store.ErrVectorIndexSourceStale
	}
	coverage.ScopedDocuments = resolution.ScopedDocuments
	coverage.CompleteDocuments = resolution.CompleteDocuments
	coverage.State = CoverageComplete
	if coverage.CompleteDocuments != coverage.ScopedDocuments {
		coverage.State = CoverageIncomplete
	}
	resolved := resolution.Candidates
	candidates := make([]Candidate, len(resolved))
	for index, item := range resolved {
		if item.VectorSpaceID != authority.VectorSpace.ID {
			return nil, coverage, policy, false, errors.New("semantic result escaped the active vector space")
		}
		candidates[index] = Candidate{Document: DocumentIdentity{VaultID: item.VaultID,
			NodeID: item.NodeID, ContentVersionID: item.ContentVersionID}, Lane: LaneSemantic,
			Rank: index + 1, Score: item.Score, Path: item.Path, VectorSpaceID: item.VectorSpaceID,
			Evidence: []EvidenceReference{{Kind: "embedding", VaultID: item.VaultID,
				NodeID: item.NodeID, NodeRevision: item.NodeRevision, ContentVersionID: item.ContentVersionID,
				VectorSpaceID: item.VectorSpaceID, EmbeddingSetID: item.EmbeddingSetID,
				InputGenerationID: item.InputGenerationID, InputID: item.InputID,
				InputKind: item.InputKind}}}
	}
	return candidates, coverage, policy, resolution.Truncated || rawTruncated, nil
}

func normalizeQuery(query Query) (Query, error) {
	query.Text = strings.TrimSpace(query.Text)
	options, err := embedding.NormalizeSearchOptions(embedding.SearchOptions{
		Mode: embedding.SearchMode(query.Mode), CandidateLimit: query.Limit,
	})
	if err != nil {
		return Query{}, err
	}
	query.Mode, query.Limit = Mode(options.Mode), options.CandidateLimit
	if store.SearchNeedsQuery(query.Text, store.SearchOptions{}) {
		if query.Mode == ModeSemantic || query.Mode == ModeHybrid {
			return Query{}, errors.New("retrieval query text is required for semantic modes")
		}
		if store.SearchNeedsQuery(query.Text, query.Scope) {
			return Query{}, store.ErrSearchQueryRequired
		}
	}
	if query.Offset < 0 {
		return Query{}, errors.New("retrieval offset must not be negative")
	}

	return query, nil
}

func (searcher *Searcher) collectLexical(ctx context.Context, query Query) ([]Candidate, bool, error) {
	if store.SearchNeedsQuery(query.Text, store.SearchOptions{}) {
		backend, ok := searcher.backend.(FilterBackend)
		if !ok {
			return nil, false, errors.New("filter-only retrieval is not configured")
		}
		hits, truncated, err := backend.SearchPageWithOptions(
			ctx, query.Text, query.Limit, query.Offset, query.Scope,
		)
		if err != nil {
			return nil, false, err
		}
		candidates := make([]Candidate, len(hits))
		for index, hit := range hits {
			candidates[index] = Candidate{
				Document: DocumentIdentity{VaultID: searcher.backend.VaultID(), NodeID: hit.Node.ID,
					ContentVersionID: hit.Node.CurrentVersionID},
				Lane: LaneLexical, Rank: query.Offset + index + 1, Path: hit.Path,
				Excerpt: hit.Node.Name, Evidence: []EvidenceReference{{Kind: "node_filter",
					VaultID: searcher.backend.VaultID(), NodeID: hit.Node.ID,
					NodeRevision: hit.Node.Revision, ContentVersionID: hit.Node.CurrentVersionID,
					BlobHash: hit.Node.BlobHash}},
			}
		}
		return candidates, truncated, nil
	}
	hits, truncated, err := searcher.backend.SearchExplainedLexicalCandidates(
		ctx, query.Text, query.Limit, query.Offset, query.Scope,
	)
	if err != nil {
		return nil, false, err
	}
	candidates := make([]Candidate, len(hits))
	for index, hit := range hits {
		candidates[index] = Candidate{Document: DocumentIdentity{VaultID: searcher.backend.VaultID(),
			NodeID: hit.Node.ID, ContentVersionID: hit.Node.CurrentVersionID}, Lane: LaneLexical,
			Rank: query.Offset + index + 1, Path: hit.Path, Excerpt: hit.Excerpt,
			Evidence: []EvidenceReference{{Kind: hit.EvidenceKind, VaultID: searcher.backend.VaultID(),
				NodeID: hit.Node.ID, ContentVersionID: hit.Node.CurrentVersionID,
				NodeRevision: hit.Node.Revision,
				BuildID:      hit.BuildID, SegmentID: hit.SegmentID, BlobHash: hit.BlobHash}}}
	}
	return candidates, truncated, nil
}

func laneReport(requested, actual Mode, coverage Coverage,
	candidates []Candidate, truncated bool, limit, offset int,
) Report {
	results := make([]Result, len(candidates))
	for index, candidate := range candidates {
		contribution := 1 / float64(ReciprocalRankK+candidate.Rank)
		result := Result{Document: candidate.Document, Rank: candidate.Rank, Score: contribution,
			Path: candidate.Path, Excerpt: candidate.Excerpt, Evidence: candidate.Evidence,
			Explanation: []Contribution{{Lane: candidate.Lane, Rank: candidate.Rank,
				Contribution: contribution}}}
		if candidate.Lane == LaneLexical {
			result.LexicalRank = candidate.Rank
		} else {
			result.SemanticRank = candidate.Rank
		}
		results[index] = result
	}
	traceCode := TraceLexicalCandidates
	if actual == ModeSemantic {
		traceCode = TraceSemanticCandidates
	}
	return Report{RequestedMode: requested, ActualMode: actual, Coverage: coverage,
		Results: results, Limit: limit, Offset: offset, NextOffset: offset + len(results),
		Truncated: truncated,
		Trace:     []TraceEvent{{Code: traceCode, Count: len(results)}}}
}

func makeHybridReport(requested Mode, coverage Coverage, lexical, semantic []Candidate,
	lexicalTruncated, semanticTruncated bool, query Query,
) (Report, error) {
	fusedLimit := candidateWindow(query.Offset, query.Limit)
	results, fusedTruncated, err := FuseReciprocalRank(lexical, semantic, fusedLimit)
	if err != nil {
		return Report{}, err
	}
	results, pageTruncated := pageSlice(results, query.Offset, query.Limit)
	report := Report{RequestedMode: requested, ActualMode: ModeHybrid, Coverage: coverage,
		Results: results, Limit: query.Limit, Offset: query.Offset,
		NextOffset: query.Offset + len(results),
		Truncated:  pageTruncated || (fusedTruncated && fusedLimit < MaxCandidateLimit),
		Trace: []TraceEvent{{Code: TraceLexicalCandidates, Count: len(lexical)},
			{Code: TraceSemanticCandidates, Count: len(semantic)},
			{Code: TraceFusedCandidates, Count: len(results)}}}
	if lexicalTruncated {
		report.SkippedReasons = append(report.SkippedReasons, SkippedLexicalCandidateLimit)
	}
	if semanticTruncated {
		report.SkippedReasons = append(report.SkippedReasons, SkippedSemanticCandidateLimit)
	}
	if fusedTruncated && fusedLimit == MaxCandidateLimit {
		report.SkippedReasons = append(report.SkippedReasons, SkippedFinalCandidateLimit)
	}
	return report, nil
}

func (searcher *Searcher) lexical(ctx context.Context, query Query, requested Mode,
	coverage Coverage,
) (Report, error) {
	candidates, truncated, err := searcher.collectLexical(ctx, query)
	if err != nil {
		return Report{}, err
	}
	return laneReport(requested, ModeLexical, coverage, candidates, truncated,
		query.Limit, query.Offset), nil
}

func pageSlice[T any](items []T, offset, limit int) ([]T, bool) {
	if offset >= len(items) {
		return []T{}, false
	}
	end := min(len(items), offset+limit)
	return items[offset:end], end < len(items)
}

func candidateWindow(offset, limit int) int {
	if offset >= MaxCandidateLimit || limit >= MaxCandidateLimit-offset {
		return MaxCandidateLimit
	}
	return offset + limit + 1
}
