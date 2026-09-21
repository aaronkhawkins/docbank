package main

import (
	"slices"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/retrieval"
	"go.kenn.io/docbank/internal/store"
)

func newDaemonSearchService(cfg config.Config, backend *store.Store,
	runtimes *processing.EmbeddingRuntimeRegistry,
) (*retrieval.Service, error) {
	searcher, err := retrieval.NewSearcher(retrieval.SearcherConfig{
		Backend: backend, Encoders: runtimes, Owner: "daemon-document-search",
		LeaseDuration: 5 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	binding, unavailable, err := configuredSemanticSearchBinding(cfg, runtimes)
	if err != nil {
		return nil, err
	}
	return retrieval.NewService(searcher, binding, unavailable), nil
}

func configuredSemanticSearchBinding(cfg config.Config,
	runtimes *processing.EmbeddingRuntimeRegistry,
) (*retrieval.SemanticBinding, retrieval.FallbackReason, error) {
	descriptors := make(map[string]document.EmbeddingDescriptor)
	for _, descriptor := range runtimes.QueryDescriptors() {
		descriptors[descriptor.Fingerprint] = descriptor
	}
	profileNames := make([]string, 0, len(cfg.ProcessingProfiles))
	for name := range cfg.ProcessingProfiles {
		profileNames = append(profileNames, name)
	}
	slices.Sort(profileNames)
	candidates := make([]retrieval.SemanticBinding, 0)
	for _, name := range profileNames {
		profile, err := cfg.ProcessingProfile(name)
		if err != nil {
			return nil, "", err
		}
		_, fingerprints, err := document.CanonicalProfile(profile.Document)
		if err != nil {
			return nil, "", err
		}
		for _, embedding := range profile.Document.Embeddings {
			descriptor, ok := descriptors[embedding.Descriptor.Fingerprint]
			if !ok || descriptor.ID != embedding.Descriptor.ID {
				continue
			}
			candidates = append(candidates, retrieval.SemanticBinding{
				ProcessingProfileFingerprint: fingerprints.Profile,
				BindingID:                    embedding.Name,
				Authorization: document.EmbeddingAuthorization{
					ProviderID: descriptor.ID, DescriptorFingerprint: descriptor.Fingerprint,
					PolicyFingerprint: descriptor.PolicyFingerprint, MaxBatchItems: 1,
					MaxInputBytes: embedding.MaxInputBytes, MaxResponseBytes: embedding.MaxResponseBytes,
				},
			})
		}
	}
	binding, reason := selectSemanticSearchBinding(candidates)
	return binding, reason, nil
}

func selectSemanticSearchBinding(candidates []retrieval.SemanticBinding) (
	*retrieval.SemanticBinding, retrieval.FallbackReason,
) {
	switch len(candidates) {
	case 0:
		return nil, retrieval.FallbackSemanticNotConfigured
	case 1:
		return &candidates[0], ""
	default:
		return nil, retrieval.FallbackSemanticBindingAmbiguous
	}
}
