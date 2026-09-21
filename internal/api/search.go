package api

import (
	"errors"
	"net/http"

	"go.kenn.io/docbank/internal/retrieval"
	"go.kenn.io/docbank/internal/store"
)

func searchResultMatch(result retrieval.Result) string {
	switch {
	case result.LexicalRank > 0 && result.SemanticRank > 0:
		return "hybrid"
	case result.SemanticRank > 0:
		return "semantic"
	}
	for _, evidence := range result.Evidence {
		switch evidence.Kind {
		case "node_name":
			return store.SearchMatchName
		case "node_filter":
			return store.SearchMatchFilter
		}
	}
	return store.SearchMatchContent
}

func validVaultDirectoryScope(vaultID string, underNodeID int64) bool {
	return (vaultID == "" && underNodeID == 0) || (vaultID != "" && underNodeID > 0)
}

func searchResultMatchesNode(result retrieval.Result, node store.Node, vaultID string) bool {
	if node.TrashedAt != nil || result.Document.VaultID != vaultID ||
		result.Document.NodeID != node.ID || result.Document.ContentVersionID != node.CurrentVersionID {
		return false
	}
	for _, evidence := range result.Evidence {
		if evidence.VaultID != vaultID || evidence.NodeID != node.ID ||
			evidence.NodeRevision != node.Revision || evidence.ContentVersionID != node.CurrentVersionID {
			return false
		}
	}
	return len(result.Evidence) > 0
}

func fromRetrievalError(err error) error {
	switch {
	case errors.Is(err, retrieval.ErrSemanticUnavailable):
		return NewError(http.StatusServiceUnavailable, "semantic_unavailable",
			"semantic search prerequisites are unavailable")
	case errors.Is(err, store.ErrSearchQueryRequired):
		return NewError(http.StatusUnprocessableEntity, "search_query_required",
			store.ErrSearchQueryRequired.Error())
	case errors.Is(err, store.ErrVectorIndexSourceStale):
		return NewError(http.StatusConflict, "semantic_authority_stale",
			"semantic search authority changed; retry after index reconciliation")
	}
	for _, mapped := range storeErrCodes {
		if errors.Is(err, mapped.target) {
			return NewError(mapped.status, mapped.code, err.Error())
		}
	}
	return NewError(http.StatusServiceUnavailable, "retrieval_failed",
		"document retrieval failed")
}
