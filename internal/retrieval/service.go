package retrieval

import (
	"context"
	"errors"
	"strings"

	"go.kenn.io/docbank/document"
)

// SemanticBinding is the daemon-selected, non-public authority needed for one
// exact semantic retrieval binding.
type SemanticBinding struct {
	ProcessingProfileFingerprint string
	BindingID                    string
	Authorization                document.EmbeddingAuthorization
	AcrossProfiles               bool
}

type SemanticBindingResolver func(context.Context) (*SemanticBinding, FallbackReason, error)

// Service keeps public requests free of internal fingerprints while allowing
// lexical operation when no semantic binding is executable.
type Service struct {
	searcher          *Searcher
	binding           *SemanticBinding
	resolveBinding    SemanticBindingResolver
	unavailableReason FallbackReason
}

func NewServiceWithBindingResolver(searcher *Searcher, resolver SemanticBindingResolver) *Service {
	return &Service{searcher: searcher, resolveBinding: resolver, unavailableReason: FallbackSemanticNotConfigured}
}

func NewService(searcher *Searcher, binding *SemanticBinding, unavailableReason FallbackReason) *Service {
	if unavailableReason == "" {
		unavailableReason = FallbackSemanticNotConfigured
	}
	return &Service{searcher: searcher, binding: binding, unavailableReason: unavailableReason}
}

func (service *Service) Search(ctx context.Context, query Query) (Report, error) {
	if service == nil || service.searcher == nil {
		return Report{}, errors.New("retrieval service is not configured")
	}
	requested := query.Mode
	if requested == "" {
		requested = ModeAuto
	}
	if requested != ModeSemantic && requested != ModeHybrid {
		query.ProcessingProfileFingerprint = ""
		query.BindingID = ""
		query.Authorization = document.EmbeddingAuthorization{}
		query.AcrossProfiles = false
		return service.searcher.Search(ctx, query)
	}
	if strings.TrimSpace(query.Text) == "" {
		return Report{}, errors.New("retrieval query text is required for semantic modes")
	}
	binding := service.binding
	reason := service.unavailableReason
	if service.resolveBinding != nil {
		var err error
		binding, reason, err = service.resolveBinding(ctx)
		if err != nil {
			return Report{}, err
		}
	}
	if reason == "" {
		reason = FallbackSemanticNotConfigured
	}
	if binding == nil {
		unavailable := &semanticUnavailableError{reason: reason,
			cause: errors.New("no semantic binding is executable")}
		if requested == ModeSemantic {
			return Report{}, unavailable
		}
		query.Mode = ModeLexical
		report, err := service.searcher.Search(ctx, query)
		if err != nil {
			return Report{}, err
		}
		report.RequestedMode = requested
		report.Fallback = Fallback{Applied: true, Reason: reason}
		return report, nil
	}
	query.ProcessingProfileFingerprint = binding.ProcessingProfileFingerprint
	query.BindingID = binding.BindingID
	query.Authorization = binding.Authorization
	query.AcrossProfiles = binding.AcrossProfiles
	return service.searcher.Search(ctx, query)
}
