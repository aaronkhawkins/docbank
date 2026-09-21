package api_test

import (
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/internal/api"
)

func TestOpenAPIDocumentOffline(t *testing.T) {
	// No store, no blobs, no listener: registration must not touch deps.
	out, err := api.OpenAPIYAML()
	require.NoError(t, err)
	doc := string(out)
	for _, op := range []string{"getNode", "resolvePath", "listChildren", "getNodeContent", "verifyNodeContent",
		"listContentVersions", "getContentVersion", "getContentVersionBytes", "pruneNodeContentVersions",
		"lookupContentReferences",
		"listTags", "resolveTagByName", "getTag", "listTagNodes", "listNodeTags",
		"createTag", "renameTag", "deleteTag", "assignTag", "unassignTag",
		"assignTagPath", "unassignTagPath",
		"previewAuditEnrollment", "enableAudit", "auditStatus", "auditNodeHistory", "verifyAudit",
		"search", "createNode", "moveNode", "movePath", "trashNode", "trashPath", "restoreNode",
		"storageStatus", "storagePack", "storageRepack", "ingest", "uploadFile", "publishSuppliedOCR",
		"listTrash", "emptyTrash", "gc", "verify",
		"initBackupRepository", "createBackupSnapshot", "listBackupSnapshots", "listJobs"} {
		assert.Contains(t, doc, op, "operation missing from OpenAPI doc")
	}
	assert.NotContains(t, doc, "/api/daemon/shutdown", "lifecycle plumbing must stay hidden")
	assert.NotContains(t, doc, "/api/daemon/challenge", "lifecycle plumbing must stay hidden")
	assert.Contains(t, doc, "X-Docbank-Blob-Hash")
	assert.Contains(t, doc, api.ContentVersionHeader)
	assert.Contains(t, doc, "Content-Digest")
	assert.Contains(t, doc, "computed_hash")
	for _, schema := range []string{"IngestPreflightRequest", "IngestRequest"} {
		block := openAPISchemaBlock(t, doc, schema)
		assert.Contains(t, block, "        include:")
		assert.Contains(t, block, "        exclude:")
		if schema == "IngestRequest" {
			assert.Contains(t, block, "        dest:")
		} else {
			assert.NotContains(t, block, "        dest:")
		}
	}
}

func openAPISchemaBlock(t *testing.T, doc, schema string) string {
	t.Helper()
	marker := "\n    " + schema + ":\n"
	start := strings.Index(doc, marker)
	require.NotEqual(t, -1, start, "schema missing from OpenAPI document")
	block := doc[start+len(marker):]
	for offset := strings.Index(block, "\n"); offset >= 0; {
		line := block[offset+1:]
		if line != "" && !strings.HasPrefix(line, "      ") {
			block = block[:offset]
			break
		}
		if strings.HasPrefix(line, "    ") && len(line) > 4 && line[4] != ' ' {
			block = block[:offset]
			break
		}
		next := strings.Index(line, "\n")
		if next < 0 {
			break
		}
		offset += next + 1
	}
	return block
}

func TestOpenAPIDeclaresSecurity(t *testing.T) {
	// Generated clients must learn from the document alone that every
	// operation needs credentials (X-Api-Key header or bearer token).
	out, err := api.OpenAPIYAML()
	require.NoError(t, err)
	doc := string(out)
	assert.Contains(t, doc, "securitySchemes")
	assert.Contains(t, doc, "X-Api-Key")
	assert.Contains(t, doc, "scheme: bearer")
	assert.Contains(t, doc, "security:", "document-level security requirement missing")
}

func TestOpenAPISearchQueryIsOptional(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	op := doc.Paths["/api/v1/search"].Get
	require.NotNil(t, op)
	for _, param := range op.Parameters {
		if param.Name == "q" {
			assert.False(t, param.Required)
			return
		}
	}
	t.Fatal("search q parameter missing")
}

func TestOpenAPISearchDeclaresRetrievalModesAndEvidence(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	op := doc.Paths["/api/v1/search"].Get
	require.NotNil(t, op)
	var mode *huma.Param
	for _, parameter := range op.Parameters {
		if parameter.Name == "mode" {
			mode = parameter
			break
		}
	}
	require.NotNil(t, mode)
	assert.ElementsMatch(t, []any{"auto", "lexical", "semantic", "hybrid"}, mode.Schema.Enum)
	report := doc.Components.Schemas.Map()["SearchReport"]
	require.NotNil(t, report)
	for _, field := range []string{"requested_mode", "actual_mode", "coverage", "fallback",
		"skipped_reasons", "trace", "hits", "offset", "next_offset", "truncated"} {
		assert.Contains(t, report.Properties, field)
	}
	hit := doc.Components.Schemas.Map()["SearchHit"]
	require.NotNil(t, hit)
	for _, field := range []string{"rank", "score", "excerpt", "lexical_rank", "semantic_rank",
		"evidence", "explanation"} {
		assert.Contains(t, hit.Properties, field)
	}
}

func TestOpenAPISearchPaginationContract(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	for _, path := range []string{"/api/v1/search", "/api/v1/evidence/search"} {
		op := doc.Paths[path].Get
		require.NotNil(t, op)
		var offset *huma.Param
		for _, param := range op.Parameters {
			if param.Name == "offset" {
				offset = param
				break
			}
		}
		require.NotNil(t, offset, "%s offset parameter missing", path)
		require.NotNil(t, offset.Schema.Minimum)
		assert.InDelta(t, 0, *offset.Schema.Minimum, 0)
		assert.Equal(t, 0, offset.Schema.Default)
	}
	for _, name := range []string{"SearchReport", "EvidenceSearchReport"} {
		schema := doc.Components.Schemas.Map()[name]
		require.NotNil(t, schema)
		for _, field := range []string{"offset", "next_offset", "truncated"} {
			assert.Contains(t, schema.Properties, field, "%s.%s missing", name, field)
		}
		for _, field := range []string{"offset", "next_offset"} {
			require.NotNil(t, schema.Properties[field].Minimum)
			assert.InDelta(t, 0, *schema.Properties[field].Minimum, 0)
		}
	}
}

func TestLongRunningBackupRoutesClearBodyReadDeadline(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	for _, operation := range []*huma.Operation{
		doc.Paths["/api/v1/backup/snapshots"].Post,
		doc.Paths["/api/v1/backup/snapshots/stream"].Post,
		doc.Paths["/api/v1/backup/verify"].Post,
		doc.Paths["/api/v1/backup/verify/stream"].Post,
		doc.Paths["/api/v1/backup/restore"].Post,
		doc.Paths["/api/v1/backup/restore/stream"].Post,
	} {
		require.NotNil(t, operation)
		assert.Negative(t, operation.BodyReadTimeout)
	}
}

func TestOpenAPIAuditEnableDisclosesCompleteRetention(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	op := doc.Paths["/api/v1/audit/enable"].Post
	require.NotNil(t, op)
	for _, class := range []string{"names", "topology", "tags", "assignments", "ingests", "provenance"} {
		assert.Contains(t, op.Description, class)
	}
}

func TestOpenAPIDeclaresDigestCheckedUpload(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	op := doc.Paths["/api/v1/uploads"].Post
	require.NotNil(t, op)
	assert.Equal(t, "uploadFile", op.OperationID)
	form := op.RequestBody.Content["multipart/form-data"]
	require.NotNil(t, form)
	assert.Equal(t, "binary", form.Schema.Properties["file"].Format)
	assert.Contains(t, form.Schema.Required, "file")

	required := map[string]bool{}
	for _, param := range op.Parameters {
		required[param.Name] = param.Required
	}
	for _, name := range []string{"parent_id", "name", api.BlobHashHeader, api.BlobSizeHeader} {
		assert.True(t, required[name], "%s must be required", name)
	}
	assert.NotNil(t, op.Responses["200"])
	assert.NotNil(t, op.Responses["201"])

	replace := doc.Paths["/api/v1/nodes/{id}/content"].Put
	require.NotNil(t, replace)
	require.NotNil(t, replace.RequestBody)
	assert.Contains(t, replace.RequestBody.Content, "*/*")
	assert.NotNil(t, replace.Responses["200"])

	revert := doc.Paths["/api/v1/nodes/{id}/revert"].Post
	require.NotNil(t, revert)
	require.NotNil(t, revert.RequestBody)
	assert.Contains(t, revert.RequestBody.Content, "application/json")
	assert.NotNil(t, revert.Responses["200"])
}

func TestOpenAPIDeclaresSuppliedOCRPublication(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	op := doc.Paths["/api/v1/nodes/{id}/supplied-ocr"].Post
	require.NotNil(t, op)
	assert.Equal(t, "publishSuppliedOCR", op.OperationID)
	form := op.RequestBody.Content["multipart/form-data"]
	require.NotNil(t, form)
	for _, part := range []string{"metadata", "transcript", "structured"} {
		assert.Equal(t, "binary", form.Schema.Properties[part].Format)
		assert.Contains(t, form.Schema.Required, part)
	}
	assert.NotNil(t, op.Responses["200"])
	assert.NotNil(t, op.Responses["201"])
	metadata := doc.Components.Schemas.Map()["SuppliedOCRPublicationMetadata"]
	require.NotNil(t, metadata)
	for _, field := range []string{"content_version_id", "source_sha256", "submission_key",
		"transcript_sha256", "transcript_bytes", "structured_sha256", "structured_bytes"} {
		assert.Contains(t, metadata.Properties, field)
	}
}

func TestOpenAPIDeclaresMutationPreconditions(t *testing.T) {
	doc := api.NewOfflineServer().API().OpenAPI()
	pruneRequest := doc.Components.Schemas.Map()["VersionPruneRequest"]
	require.NotNil(t, pruneRequest)
	versionIDs := pruneRequest.Properties["version_ids"]
	require.NotNil(t, versionIDs)
	require.NotNil(t, versionIDs.MaxItems)
	assert.Equal(t, 1000, *versionIDs.MaxItems)
	assert.True(t, versionIDs.UniqueItems)
	require.NotNil(t, versionIDs.Items)
	assert.Equal(t, "uuid", versionIDs.Items.Format)
	keepNewest := pruneRequest.Properties["keep_newest"]
	require.NotNil(t, keepNewest)
	require.NotNil(t, keepNewest.Minimum)
	assert.InDelta(t, 1, *keepNewest.Minimum, 0)
	for _, operation := range []*huma.Operation{
		doc.Paths["/api/v1/nodes/{id}"].Patch,
		doc.Paths["/api/v1/nodes/{id}/trash"].Post,
		doc.Paths["/api/v1/nodes/{id}/restore"].Post,
		doc.Paths["/api/v1/nodes/{id}/verify"].Post,
		doc.Paths["/api/v1/nodes/{id}/revert"].Post,
		doc.Paths["/api/v1/nodes/{id}/versions/prune"].Post,
		doc.Paths["/api/v1/nodes/{id}/tags/{tag_id}"].Put,
		doc.Paths["/api/v1/nodes/{id}/tags/{tag_id}"].Delete,
		doc.Paths["/api/v1/tags/{tag_id}"].Patch,
		doc.Paths["/api/v1/tags/{tag_id}"].Delete,
	} {
		require.NotNil(t, operation)
		required := map[string]bool{}
		for _, parameter := range operation.Parameters {
			required[parameter.Name] = parameter.Required
		}
		assert.True(t, required["If-Match"])
	}
	create := doc.Paths["/api/v1/tags"].Post
	require.NotNil(t, create)
	assert.NotNil(t, create.Responses["201"])
}
