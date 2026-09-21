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
}

// Service keeps public requests free of internal fingerprints while allowing
// lexical operation when no semantic binding is executable.
type Service struct {
	searcher          *Searcher
	binding           *SemanticBinding
	unavailableReason FallbackReason
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
		return service.searcher.Search(ctx, query)
	}
	if strings.TrimSpace(query.Text) == "" {
		return Report{}, errors.New("retrieval query text is required for semantic modes")
	}
	if service.binding == nil {
		unavailable := &semanticUnavailableError{reason: service.unavailableReason,
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
		report.Fallback = Fallback{Applied: true, Reason: service.unavailableReason}
		return report, nil
	}
	query.ProcessingProfileFingerprint = service.binding.ProcessingProfileFingerprint
	query.BindingID = service.binding.BindingID
	query.Authorization = service.binding.Authorization
	return service.searcher.Search(ctx, query)
}
