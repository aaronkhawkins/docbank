package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/kit/pack"
	"go.kenn.io/kit/packstore"

	"go.kenn.io/docbank/internal/retrieval"
	"go.kenn.io/docbank/internal/store"
)

type nodeOutput struct {
	ETag string `header:"ETag"`
	Body Node
}

func contentDigest(hash []byte) string {
	return "sha-256=:" + base64.StdEncoding.EncodeToString(hash) + ":"
}

func isContentCorruption(err error) bool {
	return errors.Is(err, packstore.ErrContentMismatch) ||
		errors.Is(err, pack.ErrTruncated) ||
		errors.Is(err, pack.ErrChecksum) ||
		errors.Is(err, pack.ErrCorrupt) ||
		errors.Is(err, pack.ErrBlobMismatch)
}

func staleRevisionError(id, expected, actual int64) error {
	return FromStoreError(fmt.Errorf("node %d revision is %d, expected %d: %w",
		id, actual, expected, store.ErrStaleRevision))
}

// nodeOutputAt builds the single-node response with a caller-supplied
// display path (used where the store-computed path would mislead, e.g.
// trash responses reporting the pre-trash location).
func nodeOutputAt(n store.Node, path string) *nodeOutput {
	body := fromStoreNode(n)
	body.Path = path
	return &nodeOutput{ETag: fmt.Sprintf("%q", strconv.FormatInt(n.Revision, 10)), Body: body}
}

// nodeWithPath loads the node's display path and builds the single-node
// response. Every single-node endpoint returns this shape.
func nodeWithPath(ctx context.Context, d Deps, id int64) (*nodeOutput, error) {
	view, err := d.Store.NodeSourceMetadataViewByID(ctx, id)
	if err != nil {
		return nil, FromStoreError(err)
	}
	return nodeOutputFromSourceMetadataView(ctx, view), nil
}

func nodeOutputFromSourceMetadataView(ctx context.Context, view store.NodeSourceMetadataView) *nodeOutput {
	out := nodeOutputAt(view.Node, view.Path)
	if view.SourceMetadata != nil {
		out.Body.SourceMetadata = fromStoreSourceMetadata(*view.SourceMetadata, browserSessionRequest(ctx))
	}
	return out
}

func contentResponses() map[string]*huma.Response {
	return map[string]*huma.Response{
		"200": {
			Description: "Document bytes with expected version and blob identity plus a computed digest trailer",
			Headers: map[string]*huma.Param{
				ContentVersionHeader: {Description: "Stable content-version UUID",
					Schema: &huma.Schema{Type: openAPIStringType, Format: "uuid"}},
				BlobHashHeader: {Description: "Catalog SHA-256 identity (lowercase hex)",
					Schema: &huma.Schema{Type: openAPIStringType, Pattern: "^[0-9a-f]{64}$"}},
				BlobSizeHeader: {Description: "Catalog raw byte length",
					Schema: &huma.Schema{Type: openAPIStringType, Pattern: "^[0-9]+$"}},
				"Content-Digest": {Description: "RFC 9530 SHA-256 of bytes actually streamed; delivered as an HTTP trailer",
					Schema: &huma.Schema{Type: openAPIStringType}},
			},
			Content: map[string]*huma.MediaType{
				"application/octet-stream": {Schema: &huma.Schema{Type: openAPIStringType, Format: "binary"}},
			},
		},
	}
}

func streamContentVersion(
	ctx context.Context, d Deps, version store.ContentVersion,
) (*huma.StreamResponse, error) {
	f, _, err := d.Blobs.OpenStreamContext(ctx, version.BlobHash)
	if err != nil {
		return nil, NewError(http.StatusInternalServerError, "internal",
			fmt.Sprintf("opening blob %s: %v (run docbank verify)", version.BlobHash, err))
	}
	contentType := version.MimeType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() { _ = f.Close() }()
		hctx.SetHeader("Content-Type", contentType)
		hctx.SetHeader(ContentVersionHeader, version.ID)
		hctx.SetHeader(BlobHashHeader, version.BlobHash)
		hctx.SetHeader(BlobSizeHeader, strconv.FormatInt(version.Size, 10))
		hctx.SetHeader("Trailer", "Content-Digest")
		hash := sha256.New()
		if _, err := io.Copy(hctx.BodyWriter(), io.TeeReader(f, hash)); err == nil {
			hctx.SetHeader("Content-Digest", contentDigest(hash.Sum(nil)))
		}
	}}, nil
}

func registerReadRoutes(api huma.API, d Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "getNode", Method: http.MethodGet, Path: "/api/v1/nodes/{id}",
		Summary: "Stat a node by id (live or trashed)",
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*nodeOutput, error) {
		return nodeWithPath(ctx, d, in.ID)
	})

	type contentVersionsOutput struct{ Body ContentVersionPage }
	huma.Register(api, huma.Operation{
		OperationID: "listContentVersions", Method: http.MethodGet,
		Path:    "/api/v1/nodes/{id}/versions",
		Summary: "List a file's immutable content versions, newest first",
	}, func(ctx context.Context, in *struct {
		ID     int64 `path:"id"`
		Limit  int   `query:"limit" default:"100" minimum:"1" maximum:"1000"`
		Offset int   `query:"offset" default:"0" minimum:"0"`
	}) (*contentVersionsOutput, error) {
		versions, total, err := d.Store.ContentVersions(ctx, in.ID, in.Limit, in.Offset)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &contentVersionsOutput{Body: ContentVersionPage{
			Items: []ContentVersion{}, Total: total, Limit: in.Limit, Offset: in.Offset,
		}}
		for _, version := range versions {
			out.Body.Items = append(out.Body.Items, fromStoreContentVersion(version))
		}
		return out, nil
	})

	type contentVersionOutput struct{ Body ContentVersion }
	huma.Register(api, huma.Operation{
		OperationID: "getContentVersion", Method: http.MethodGet,
		Path:    "/api/v1/versions/{version_id}",
		Summary: "Inspect one immutable content version by stable ID",
	}, func(ctx context.Context, in *struct {
		VersionID string `path:"version_id"`
	}) (*contentVersionOutput, error) {
		version, err := d.Store.ContentVersionByID(ctx, in.VersionID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		body := fromStoreContentVersion(version)
		metadata, metadataErr := d.Store.ContentVersionSourceMetadata(ctx, version.ID)
		if metadataErr == nil {
			body.SourceMetadata = fromStoreSourceMetadata(metadata, browserSessionRequest(ctx))
		}
		if metadataErr != nil && !errors.Is(metadataErr, store.ErrNotFound) &&
			!errors.Is(metadataErr, store.ErrSourceMetadataCorrupt) {
			return nil, FromStoreError(metadataErr)
		}
		return &contentVersionOutput{Body: body}, nil
	})

	type documentViewerOutput struct{ Body DocumentViewer }
	huma.Register(api, huma.Operation{
		OperationID: "getDocumentViewer", Method: http.MethodGet,
		Path:    "/api/v1/nodes/{id}/versions/{version_id}/viewer",
		Summary: "Read one exact document version for the human viewer",
		Description: "Binds the stable node, retained immutable version, and a bounded page " +
			"from its newest active OCR or transcript rendition in one read snapshot.",
	}, func(ctx context.Context, in *struct {
		ID        int64  `path:"id" minimum:"1"`
		VersionID string `path:"version_id"`
		BuildID   string `query:"build_id" pattern:"^[0-9a-f]{64}$"`
		Limit     int    `query:"limit" default:"100" minimum:"1" maximum:"100"`
		Offset    int    `query:"offset" default:"0" minimum:"0"`
	}) (*documentViewerOutput, error) {
		view, err := d.Store.DocumentViewer(
			ctx, in.ID, in.VersionID, in.BuildID, in.Limit, in.Offset,
		)
		if err != nil {
			return nil, FromStoreError(err)
		}
		node := fromStoreNode(view.Node)
		node.Path = view.Path
		body := DocumentViewer{Node: node, Version: fromStoreContentVersion(view.Version)}
		if view.Rendition != nil {
			rendition := view.Rendition
			body.Rendition = &DocumentViewerRendition{
				BuildID: rendition.BuildID, SourceSHA256: rendition.SourceSHA256,
				EvidenceChecksum: rendition.EvidenceChecksum, Completeness: rendition.Completeness,
				BuildTruncated: rendition.BuildTruncated,
				Warnings:       append([]string(nil), rendition.Warnings...), PublishedAt: rendition.PublishedAt,
				Segments: []RenditionTextSegment{}, Total: rendition.Total,
				Limit: rendition.Limit, Offset: rendition.Offset,
			}
			for _, segment := range rendition.Segments {
				body.Rendition.Segments = append(body.Rendition.Segments, RenditionTextSegment{
					ID: segment.ID, UnitID: segment.UnitID, Order: segment.Order,
					CharStart: segment.CharStart, CharEnd: segment.CharEnd,
					Checksum: segment.Checksum, Text: segment.Text,
				})
			}
		}
		return &documentViewerOutput{Body: body}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getContentVersionBytes", Method: http.MethodGet,
		Path:    "/api/v1/versions/{version_id}/content",
		Summary: "Stream one immutable content version by stable ID",
		Description: "Version, blob hash, and size headers identify expected authority before the body. " +
			"Content-Digest is an RFC 9530 trailer computed from the bytes actually streamed.",
		Responses: contentResponses(),
	}, func(ctx context.Context, in *struct {
		VersionID string `path:"version_id"`
	}) (*huma.StreamResponse, error) {
		version, err := d.Store.ContentVersionByID(ctx, in.VersionID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return streamContentVersion(ctx, d, version)
	})

	type contentReferencesOutput struct{ Body ContentReferencePage }
	huma.Register(api, huma.Operation{
		OperationID: "lookupContentReferences", Method: http.MethodGet,
		Path:    "/api/v1/content-references",
		Summary: "Find stable document versions that retain a SHA-256 identity",
		Description: "Results are logical content-version references, not physical loose or pack entries. " +
			"Live current references sort first, followed by live history and trashed references.",
	}, func(ctx context.Context, in *struct {
		SHA256 string `query:"sha256" required:"true" pattern:"^[0-9a-f]{64}$"`
		Limit  int    `query:"limit" default:"100" minimum:"1" maximum:"1000"`
		Offset int    `query:"offset" default:"0" minimum:"0"`
	}) (*contentReferencesOutput, error) {
		references, total, err := d.Store.ContentReferencesByHash(
			ctx, in.SHA256, in.Limit, in.Offset,
		)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &contentReferencesOutput{Body: ContentReferencePage{
			Items: []ContentReference{}, Total: total, Limit: in.Limit, Offset: in.Offset,
		}}
		for _, ref := range references {
			out.Body.Items = append(out.Body.Items, fromStoreContentReference(ref))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "resolvePath", Method: http.MethodGet, Path: "/api/v1/path",
		Summary: "Resolve an absolute virtual path to its node",
		Description: "path is a query parameter (one well-defined encoding; " +
			"catch-all URL segments are ambiguous for encoded slashes). " +
			"Must start with '/'; '/' resolves the root.",
	}, func(ctx context.Context, in *struct {
		Path string `query:"path" required:"true" example:"/inbox/doc.pdf"`
	}) (*nodeOutput, error) {
		if !strings.HasPrefix(in.Path, "/") {
			return nil, NewError(http.StatusUnprocessableEntity, "validation",
				fmt.Sprintf("path %q must be absolute (start with /)", in.Path))
		}
		view, err := d.Store.NodeSourceMetadataViewByPath(ctx, in.Path)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return nodeOutputFromSourceMetadataView(ctx, view), nil
	})

	type childrenPage struct {
		Body NodePage
	}
	huma.Register(api, huma.Operation{
		OperationID: "listChildren", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/children",
		Summary: "List a directory's live children (dirs first, name-sorted), paginated",
	}, func(ctx context.Context, in *struct {
		ID     int64 `path:"id"`
		Limit  int   `query:"limit" default:"500" minimum:"1" maximum:"5000"`
		Offset int   `query:"offset" default:"0" minimum:"0"`
	}) (*childrenPage, error) {
		page, err := d.Store.DirectoryChildrenPage(ctx, in.ID, in.Limit, in.Offset)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &childrenPage{}
		out.Body.Directory = fromStoreNode(page.Directory.Node)
		out.Body.Directory.Path = page.Directory.Path
		out.Body.Total, out.Body.Limit, out.Body.Offset = page.Total, in.Limit, in.Offset
		out.Body.Items = []Node{}
		for _, child := range page.Children {
			out.Body.Items = append(out.Body.Items, fromStoreNode(child))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getNodeContent", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/content",
		Summary: "Stream a file's bytes",
		Description: "X-Docbank-Content-Version, X-Docbank-Blob-Hash, and X-Docbank-Blob-Size carry catalog identity " +
			"before the body. Content-Digest is an RFC 9530 trailer computed from the bytes " +
			"actually streamed; the standard Content-Length header is omitted so HTTP/1.1 " +
			"can transmit that trailer without a second physical read.",
		Responses: contentResponses(),
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*huma.StreamResponse, error) {
		n, err := d.Store.NodeByID(ctx, in.ID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		if n.IsDir() {
			return nil, NewError(http.StatusUnprocessableEntity, "not_file",
				fmt.Sprintf("node %d is a directory", n.ID))
		}
		version, err := d.Store.ContentVersionByID(ctx, n.CurrentVersionID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		return streamContentVersion(ctx, d, version)
	})

	type evidenceSearchOutput struct{ Body EvidenceSearchReport }
	huma.Register(api, huma.Operation{
		OperationID: "searchLexicalEvidence", Method: http.MethodGet,
		Path: "/api/v1/evidence/search", Summary: "Search live documents with immutable lexical evidence",
		Description: "This endpoint is explicitly lexical. Each result identifies either the node name, " +
			"the original content blob, or an immutable rendition build and segment.",
	}, func(ctx context.Context, in *struct {
		Q      string `query:"q" required:"true" minLength:"1" maxLength:"4096"`
		Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
		Offset int    `query:"offset" default:"0" minimum:"0"`
	}) (*evidenceSearchOutput, error) {
		hits, truncated, err := d.Store.SearchExplainedLexicalCandidates(
			ctx, in.Q, in.Limit, in.Offset, store.SearchOptions{},
		)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &evidenceSearchOutput{Body: EvidenceSearchReport{
			Mode: "lexical", Hits: []EvidenceSearchHit{}, Limit: in.Limit,
			Offset: in.Offset, NextOffset: in.Offset + len(hits), Truncated: truncated,
		}}
		for _, hit := range hits {
			out.Body.Hits = append(out.Body.Hits, EvidenceSearchHit{
				Node: fromStoreNode(hit.Node), Path: hit.Path, Match: hit.Match,
				EvidenceKind: hit.EvidenceKind, BuildID: hit.BuildID, SegmentID: hit.SegmentID,
				BlobHash: hit.BlobHash, Excerpt: hit.Excerpt,
			})
		}
		return out, nil
	})

	type renditionTextOutput struct{ Body RenditionTextPage }
	huma.Register(api, huma.Operation{
		OperationID: "readRenditionText", Method: http.MethodGet,
		Path:    "/api/v1/renditions/{build_id}/text",
		Summary: "Read bounded normalized text from one immutable rendition build",
	}, func(ctx context.Context, in *struct {
		BuildID string `path:"build_id" pattern:"^[0-9a-f]{64}$"`
		Limit   int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
		Offset  int    `query:"offset" default:"0" minimum:"0"`
	}) (*renditionTextOutput, error) {
		page, err := d.Store.RenditionText(ctx, in.BuildID, in.Limit, in.Offset)
		if err != nil {
			return nil, FromStoreError(err)
		}
		out := &renditionTextOutput{Body: RenditionTextPage{
			BuildID: page.BuildID, SourceSHA256: page.SourceSHA256,
			Completeness: page.Completeness, Truncated: page.BuildTruncated,
			Segments: []RenditionTextSegment{}, Total: page.Total, Limit: page.Limit, Offset: page.Offset,
		}}
		for _, segment := range page.Segments {
			out.Body.Segments = append(out.Body.Segments, RenditionTextSegment{
				ID: segment.ID, UnitID: segment.UnitID, Order: segment.Order,
				CharStart: segment.CharStart, CharEnd: segment.CharEnd,
				Checksum: segment.Checksum, Text: segment.Text,
			})
		}
		return out, nil
	})

	type verifyNodeOutput struct{ Body ContentVerification }
	huma.Register(api, huma.Operation{
		OperationID: "verifyNodeContent", Method: http.MethodPost, Path: "/api/v1/nodes/{id}/verify",
		Summary: "Re-hash one file and bind the evidence to its node revision",
		Description: "Requires If-Match from a prior node response. Returns catalog identity and " +
			"a fresh read through the mixed loose/packed store; a concurrent node change returns 412.",
	}, func(ctx context.Context, in *struct {
		ID      int64  `path:"id"`
		IfMatch string `header:"If-Match"`
	}) (*verifyNodeOutput, error) {
		revision, err := parseIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		n, err := d.Store.NodeByID(ctx, in.ID)
		if err != nil {
			return nil, FromStoreError(err)
		}
		if n.IsDir() {
			return nil, NewError(http.StatusUnprocessableEntity, "not_file",
				fmt.Sprintf("node %d is a directory", n.ID))
		}
		if n.Revision != revision {
			return nil, staleRevisionError(n.ID, revision, n.Revision)
		}

		report := ContentVerification{
			NodeID: n.ID, VersionID: n.CurrentVersionID, Revision: n.Revision,
			BlobHash: n.BlobHash, Size: n.Size,
		}
		f, _, openErr := d.Blobs.OpenStreamContext(ctx, n.BlobHash)
		if openErr != nil {
			if errors.Is(openErr, fs.ErrNotExist) {
				report.Problem = "missing"
			} else {
				report.Problem = "unreadable"
			}
		} else {
			hash := sha256.New()
			report.ComputedSize, err = io.Copy(hash, f)
			closeErr := f.Close()
			report.ComputedHash = hex.EncodeToString(hash.Sum(nil))
			readErr := errors.Join(err, closeErr)
			if isContentCorruption(readErr) {
				report.Problem = "corrupt"
			} else if readErr != nil {
				report.Problem = "unreadable"
			} else {
				report.Verified = report.ComputedHash == n.BlobHash && report.ComputedSize == n.Size
				if !report.Verified {
					report.Problem = "corrupt"
				}
			}
		}

		current, err := d.Store.NodeByID(ctx, in.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, FromStoreError(fmt.Errorf(
					"node %d disappeared during verification: %w", in.ID, store.ErrStaleRevision))
			}
			return nil, FromStoreError(err)
		}
		if current.Revision != revision || current.CurrentVersionID != n.CurrentVersionID {
			return nil, staleRevisionError(n.ID, revision, current.Revision)
		}
		return &verifyNodeOutput{Body: report}, nil
	})

	type searchOutput struct{ Body SearchReport }
	huma.Register(api, huma.Operation{
		OperationID: "search", Method: http.MethodGet, Path: "/api/v1/search",
		Summary: "Search live document names and extracted text",
		Description: "Name matches retain their established BM25 order and appear first; " +
			"content-only matches follow in their own BM25 order. Every hit names its match source. " +
			"The query may be empty when a tag or modification-time bound makes the page bounded; " +
			"such filter-only results are ordered by modification time descending. " +
			"An optional stable tag ID requires that assignment for every result. An optional " +
			"parameter-free MIME type matches the current file version with or without parameters. " +
			"An optional live directory node ID restricts results to its descendants. Optional " +
			"absolute modification timestamps form an inclusive-since, exclusive-before interval.",
	}, func(ctx context.Context, in *struct {
		Q              string `query:"q"`
		Mode           string `query:"mode" default:"auto" enum:"auto,lexical,semantic,hybrid"`
		Limit          int    `query:"limit" default:"50" minimum:"1" maximum:"1000"`
		Offset         int    `query:"offset" default:"0" minimum:"0"`
		TagID          string `query:"tag_id" pattern:"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"`
		MIMEType       string `query:"mime_type" maxLength:"255"`
		VaultID        string `query:"vault_id" pattern:"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"`
		UnderNodeID    int64  `query:"under_node_id" minimum:"1"`
		ModifiedSince  string `query:"modified_since" maxLength:"64"`
		ModifiedBefore string `query:"modified_before" maxLength:"64"`
	}) (*searchOutput, error) {
		if browserSessionRequest(ctx) && (in.Mode == string(retrieval.ModeSemantic) ||
			in.Mode == string(retrieval.ModeHybrid)) {
			return nil, NewError(http.StatusForbidden, "web_session_lexical_only",
				"browser sessions may use only lexical or automatic search")
		}
		if !validVaultDirectoryScope(in.VaultID, in.UnderNodeID) {
			return nil, NewError(http.StatusUnprocessableEntity, "invalid_scope",
				"vault_id and under_node_id must be supplied together")
		}
		if in.VaultID != "" && in.VaultID != d.Store.VaultID() {
			return nil, NewError(http.StatusConflict, "vault_mismatch",
				"search scope belongs to a different vault")
		}
		mimeType, err := store.NormalizeSearchMIMEType(in.MIMEType)
		if err != nil {
			return nil, NewError(http.StatusUnprocessableEntity, "validation", err.Error())
		}
		modifiedSince, modifiedBefore, err := store.NormalizeSearchTimeBounds(
			in.ModifiedSince, in.ModifiedBefore,
		)
		if err != nil {
			return nil, NewError(http.StatusUnprocessableEntity, "validation", err.Error())
		}
		if d.Search == nil {
			return nil, NewError(http.StatusServiceUnavailable, "search_unavailable",
				"document search is unavailable")
		}
		report, err := d.Search.Search(ctx, retrieval.Query{
			Text: in.Q, Mode: retrieval.Mode(in.Mode), Limit: in.Limit, Offset: in.Offset,
			Scope: store.SearchOptions{
				TagID: in.TagID, MIMEType: mimeType, UnderNodeID: in.UnderNodeID,
				ModifiedSince: modifiedSince, ModifiedBefore: modifiedBefore,
			},
		})
		if err != nil {
			return nil, fromRetrievalError(err)
		}
		out := &searchOutput{Body: SearchReport{
			VaultID:       d.Store.VaultID(),
			RequestedMode: string(report.RequestedMode), ActualMode: string(report.ActualMode),
			Coverage: SearchCoverage{BindingRequired: report.Coverage.BindingRequired,
				ScopedDocuments:   report.Coverage.ScopedDocuments,
				CompleteDocuments: report.Coverage.CompleteDocuments, State: string(report.Coverage.State)},
			Fallback:       SearchFallback{Applied: report.Fallback.Applied, Reason: string(report.Fallback.Reason)},
			SkippedReasons: make([]string, len(report.SkippedReasons)), Trace: []SearchTrace{},
			Hits: []SearchHit{}, Limit: report.Limit, Offset: report.Offset,
			NextOffset: report.NextOffset, Truncated: report.Truncated,
			TagID: in.TagID, MIMEType: mimeType, UnderNodeID: in.UnderNodeID,
			ModifiedSince: modifiedSince, ModifiedBefore: modifiedBefore,
		}}
		for index, reason := range report.SkippedReasons {
			out.Body.SkippedReasons[index] = string(reason)
		}
		for _, event := range report.Trace {
			out.Body.Trace = append(out.Body.Trace, SearchTrace{Code: string(event.Code), Count: event.Count})
		}
		nodeIDs := make([]int64, len(report.Results))
		for index, result := range report.Results {
			nodeIDs[index] = result.Document.NodeID
		}
		nodes, err := d.Store.NodesByID(ctx, nodeIDs)
		if err != nil {
			return nil, FromStoreError(err)
		}
		for _, result := range report.Results {
			node, exists := nodes[result.Document.NodeID]
			if !exists || !searchResultMatchesNode(result, node, d.Store.VaultID()) {
				return nil, NewError(http.StatusConflict, "search_authority_changed",
					"document search authority changed while rendering results")
			}
			hit := SearchHit{Node: fromStoreNode(node), Path: result.Path,
				Match: searchResultMatch(result), Rank: result.Rank, Score: result.Score,
				Excerpt: result.Excerpt, LexicalRank: result.LexicalRank,
				SemanticRank: result.SemanticRank, Evidence: []SearchEvidence{},
				Explanation: []SearchContribution{}}
			for _, evidence := range result.Evidence {
				hit.Evidence = append(hit.Evidence, SearchEvidence{Kind: evidence.Kind,
					VaultID: evidence.VaultID, NodeID: evidence.NodeID, NodeRevision: evidence.NodeRevision,
					ContentVersionID: evidence.ContentVersionID, VectorSpaceID: evidence.VectorSpaceID,
					EmbeddingSetID: evidence.EmbeddingSetID, InputGenerationID: evidence.InputGenerationID,
					InputID: evidence.InputID, InputKind: string(evidence.InputKind), BuildID: evidence.BuildID,
					SegmentID: evidence.SegmentID, BlobHash: evidence.BlobHash})
			}
			for _, explanation := range result.Explanation {
				hit.Explanation = append(hit.Explanation, SearchContribution{Lane: string(explanation.Lane),
					Rank: explanation.Rank, Contribution: explanation.Contribution})
			}
			out.Body.Hits = append(out.Body.Hits, hit)
		}
		return out, nil
	})
}
