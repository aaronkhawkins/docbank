package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/processing"
)

type suppliedOCRTestInput struct {
	nodeID         int64
	versionID      string
	sourceSHA256   string
	submissionKey  string
	transcript     []byte
	structured     []byte
	transcriptHash string
	structuredHash string
	transcriptLen  int64
	structuredLen  int64
	metadata       map[string]any
}

func suppliedOCRFixtureInput(nodeID int64, versionID, sourceSHA256 string) suppliedOCRTestInput {
	transcript := []byte("Synthetic registration\nPlate: TEST-001")
	structured := []byte(`{"schema_version":1,"markdown":"Synthetic registration\nPlate: TEST-001","layout":[{"label":"text","boxes":[[10,20,300,80]]}]}`)
	return suppliedOCRTestInput{
		nodeID: nodeID, versionID: versionID, sourceSHA256: sourceSHA256,
		submissionKey: testHash("supplied-ocr-submission"),
		transcript:    transcript, structured: structured,
		transcriptLen: int64(len(transcript)), structuredLen: int64(len(structured)),
	}
}

func suppliedOCRMetadata(in suppliedOCRTestInput) map[string]any {
	transcriptHash := in.transcriptHash
	if transcriptHash == "" {
		transcriptHash = digestTestBytes(in.transcript)
	}
	structuredHash := in.structuredHash
	if structuredHash == "" {
		structuredHash = digestTestBytes(in.structured)
	}
	metadata := map[string]any{
		"content_version_id": in.versionID,
		"source_sha256":      in.sourceSHA256,
		"submission_key":     in.submissionKey,
		"family":             "pdf", "engine": "focr", "engine_version": "0.8.0",
		"model":           "unlimited-ocr.v0.7.0.int8.focrq",
		"recipe":          "unlimited-ocr-ffn-int8-attn-bf16-lmhead-bf16-v1",
		"manifest_sha256": testHash("focr-manifest"), "manifest_bytes": int64(4_157_448_783),
		"produced_at":       "2026-09-07T12:34:56.000000000Z",
		"transcript_sha256": transcriptHash, "transcript_bytes": in.transcriptLen,
		"structured_sha256": structuredHash, "structured_bytes": in.structuredLen,
	}
	maps.Copy(metadata, in.metadata)
	return metadata
}

func suppliedPDFExtractionFixtureInput(nodeID int64, versionID, sourceSHA256 string, mixed bool) suppliedOCRTestInput {
	in := suppliedOCRFixtureInput(nodeID, versionID, sourceSHA256)
	in.transcript = []byte("Native page one\n\nNative page two")
	in.structured = []byte(`{"contract_version":"personal-os-pdf-extraction/v1","markdown":"Native page one\n\nNative page two","pages":[{"page_number":1,"source":"native_text","markdown":"Native page one"},{"page_number":2,"source":"native_text","markdown":"Native page two"}]}`)
	in.metadata = map[string]any{
		"engine": "personal-os-pdf", "engine_version": "1", "model": "none",
		"recipe": "poppler-native-or-focr-page-v1", "manifest_sha256": nil, "manifest_bytes": int64(0),
	}
	if mixed {
		focr := "{\n  \"schema_version\": 1, \"markdown\": \"Scanned page two\", \"layout\": []\n}\n"
		in.transcript = []byte("Native page one\n\nScanned page two")
		encoded, err := json.Marshal(map[string]any{
			"contract_version": "personal-os-pdf-extraction/v1", "markdown": string(in.transcript),
			"pages": []any{
				map[string]any{"page_number": 1, "source": "native_text", "markdown": "Native page one"},
				map[string]any{"page_number": 2, "source": "focr", "markdown": "Scanned page two", "focr_json": focr},
			},
		})
		if err != nil {
			panic(err)
		}
		in.structured = encoded
		in.metadata["model"] = "unlimited-ocr.v0.7.0.int8.focrq"
		in.metadata["manifest_sha256"] = "573340710167697891bf52dfa4cbb5d0a02a68f3011c01f8ef83fd34622fb592"
		in.metadata["manifest_bytes"] = int64(4_157_448_783)
	}
	in.transcriptLen = int64(len(in.transcript))
	in.structuredLen = int64(len(in.structured))
	return in
}

func digestTestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func sendSuppliedOCR(
	t *testing.T, baseURL string, client *http.Client, in suppliedOCRTestInput, apiKey *string,
) (*http.Response, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, err := json.Marshal(suppliedOCRMetadata(in), json.Deterministic(true))
	require.NoError(t, err)
	writeSuppliedOCRPart(t, writer, "metadata", "metadata.json", "application/json", metadata)
	writeSuppliedOCRPart(t, writer, "transcript", "transcript.md", "text/markdown; charset=utf-8", in.transcript)
	writeSuppliedOCRPart(t, writer, "structured", "structured.json", "application/json", in.structured)
	require.NoError(t, writer.Close())

	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/nodes/%d/supplied-ocr", baseURL, in.nodeID), &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if apiKey != nil {
		req.Header["X-Api-Key"] = []string{*apiKey}
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	response, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(response)
}

func writeSuppliedOCRPart(
	t *testing.T, writer *multipart.Writer, name, filename, contentType string, payload []byte,
) {
	t.Helper()
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition(name, filename))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
}

func TestSuppliedOCRPublicationIsAuthenticatedAndPreservesSource(t *testing.T) {
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "/source.pdf", "immutable source bytes")
	in := suppliedOCRFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash)

	emptyKey := ""
	resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, &emptyKey)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
	assert.Contains(t, body, `"code":"unauthorized"`)

	before, err := s.NodeByID(t.Context(), node.ID)
	require.NoError(t, err)
	_, versionsBefore, err := s.ContentVersions(t.Context(), node.ID, 100, 0)
	require.NoError(t, err)
	resp, body = sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var receipt api.SuppliedOCRPublicationReceipt
	require.NoError(t, json.Unmarshal([]byte(body), &receipt))
	assert.Equal(t, "added", receipt.Status)
	assert.Equal(t, node.ID, receipt.NodeID)
	assert.Equal(t, node.CurrentVersionID, receipt.ContentVersionID)
	assert.Equal(t, node.BlobHash, receipt.SourceSHA256)
	assert.Equal(t, in.submissionKey, receipt.SubmissionKey)
	assert.Len(t, receipt.MaterialChecksum, 64)
	assert.Len(t, receipt.BuildID, 64)
	assert.Len(t, receipt.AttachmentID, 64)
	assert.Len(t, receipt.LexicalGenerationID, 64)
	assert.Len(t, receipt.EvidenceChecksum, 64)

	after, err := s.NodeByID(t.Context(), node.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after, "publishing supplied OCR must not mutate original authority")
	_, versionsAfter, err := s.ContentVersions(t.Context(), node.ID, 100, 0)
	require.NoError(t, err)
	assert.Equal(t, versionsBefore, versionsAfter, "publishing supplied OCR must not add source versions")
}

func TestSuppliedOCRPublicationRejectsBindingMismatch(t *testing.T) {
	ts, s := newTestServer(t, nil)
	first := createFileWithContent(t, ts, s, "/first.pdf", "first source")
	second := createFileWithContent(t, ts, s, "/second.pdf", "second source")

	tests := []struct {
		name       string
		mutate     func(*suppliedOCRTestInput)
		wantStatus int
		wantCode   string
	}{
		{name: "version belongs to another node", mutate: func(in *suppliedOCRTestInput) {
			in.nodeID = second.ID
		}, wantStatus: http.StatusUnprocessableEntity, wantCode: "version_node_mismatch"},
		{name: "unknown version", mutate: func(in *suppliedOCRTestInput) {
			in.versionID = "11111111-1111-4111-8111-111111111111"
		}, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "source digest", mutate: func(in *suppliedOCRTestInput) {
			in.sourceSHA256 = testHash("wrong source")
		}, wantStatus: http.StatusUnprocessableEntity, wantCode: "source_digest_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := suppliedOCRFixtureInput(first.ID, first.CurrentVersionID, first.BlobHash)
			tt.mutate(&in)
			resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
			assert.Equal(t, tt.wantStatus, resp.StatusCode, body)
			assert.Contains(t, body, `"code":"`+tt.wantCode+`"`)
		})
	}
}

func TestSuppliedOCRPublicationEnforcesDeclaredArtifactBounds(t *testing.T) {
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "/source.pdf", "immutable source bytes")

	tests := []struct {
		name   string
		mutate func(*suppliedOCRTestInput)
	}{
		{name: "transcript", mutate: func(in *suppliedOCRTestInput) { in.transcriptLen = 4<<20 + 1 }},
		{name: "structured", mutate: func(in *suppliedOCRTestInput) { in.structuredLen = 1<<20 + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := suppliedOCRFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash)
			tt.mutate(&in)
			resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
			assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, body)
			assert.Contains(t, body, `"code":"too_large"`)
		})
	}
}

func TestSuppliedOCRPublicationIsIdempotentAndConflictsOnChangedMaterial(t *testing.T) {
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "/source.pdf", "immutable source bytes")
	in := suppliedOCRFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash)

	resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var added api.SuppliedOCRPublicationReceipt
	require.NoError(t, json.Unmarshal([]byte(body), &added))

	resp, body = sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var skipped api.SuppliedOCRPublicationReceipt
	require.NoError(t, json.Unmarshal([]byte(body), &skipped))
	assert.Equal(t, "skipped", skipped.Status)
	added.Status = "skipped"
	assert.Equal(t, added, skipped)
	blobsBeforeConflict, err := s.AllBlobs(t.Context())
	require.NoError(t, err)

	in.transcript = []byte("Synthetic registration\nPlate: CHANGED")
	in.transcriptLen = int64(len(in.transcript))
	in.structured = []byte(`{"schema_version":1,"markdown":"Synthetic registration\nPlate: CHANGED","layout":[{"label":"text","boxes":[[10,20,300,80]]}]}`)
	in.structuredLen = int64(len(in.structured))
	resp, body = sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, `"code":"submission_conflict"`)
	blobsAfterConflict, err := s.AllBlobs(t.Context())
	require.NoError(t, err)
	assert.Equal(t, blobsBeforeConflict, blobsAfterConflict,
		"a changed retry must fail before staging derivative blob authority")
}

func TestSuppliedOCRPublicationsForIdenticalSourcesRemainIndependent(t *testing.T) {
	ts, s := newTestServer(t, nil)
	first := createFileWithContent(t, ts, s, "/first.pdf", "identical source bytes")
	second := createFileWithContent(t, ts, s, "/second.pdf", "identical source bytes")
	for _, node := range []struct {
		id                 int64
		version, hash, key string
	}{
		{first.ID, first.CurrentVersionID, first.BlobHash, "first-submission"},
		{second.ID, second.CurrentVersionID, second.BlobHash, "second-submission"},
		{second.ID, second.CurrentVersionID, second.BlobHash, "another-submission"},
	} {
		in := suppliedOCRFixtureInput(node.id, node.version, node.hash)
		in.submissionKey = testHash(node.key)
		resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
		require.Equal(t, http.StatusCreated, resp.StatusCode, body)
		resp, body = sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
}

func TestSuppliedOCRExactLegacyRetryPreservesPublishedIdentity(t *testing.T) {
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "/legacy.pdf", "legacy source bytes")
	in := suppliedOCRFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash)
	encoded, err := json.Marshal(suppliedOCRMetadata(in))
	require.NoError(t, err)
	var metadata api.SuppliedOCRPublicationMetadata
	require.NoError(t, json.Unmarshal(encoded, &metadata))
	legacy, err := processing.BuildSuppliedOCRPublicationCandidate(processing.SuppliedOCRPublicationInput{
		VaultID: s.VaultID(), ContentVersionID: node.CurrentVersionID,
		SubmissionKey: in.submissionKey, LegacyRequestIdentity: true,
		Result: document.SuppliedOCRResult{
			Family: metadata.Family, Engine: metadata.Engine, EngineVersion: metadata.EngineVersion,
			Model: metadata.Model, Recipe: metadata.Recipe, ManifestSHA256: metadata.ManifestSHA256,
			ManifestBytes: metadata.ManifestBytes, ProducedAt: metadata.ProducedAt,
			SourceSHA256: metadata.SourceSHA256, Text: string(in.transcript), Structured: in.structured,
		},
	})
	require.NoError(t, err)
	publisher, err := processing.NewArtifactPublisher(s.Store, s.Blobs)
	require.NoError(t, err)
	published, err := publisher.PublishRendition(t.Context(), legacy.Staged)
	require.NoError(t, err)
	resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var receipt api.SuppliedOCRPublicationReceipt
	require.NoError(t, json.Unmarshal([]byte(body), &receipt))
	assert.Equal(t, "skipped", receipt.Status)
	assert.Equal(t, published.BuildID, receipt.BuildID)
	assert.Equal(t, published.AttachmentID, receipt.AttachmentID)
	assert.Equal(t, published.LexicalGeneration.ID, receipt.LexicalGenerationID)
}

func TestSuppliedOCRPublicationRejectsArtifactDigestMismatch(t *testing.T) {
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "/source.pdf", "immutable source bytes")
	in := suppliedOCRFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash)
	in.transcriptHash = testHash("wrong transcript")

	resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, body)
	assert.Contains(t, body, `"code":"digest_mismatch"`)
}

func TestSuppliedOCRPublishesNativeAndMixedPDFPageEvidence(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "native"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			ts, s := newTestServer(t, nil)
			node := createFileWithContent(t, ts, s, "/source.pdf", "immutable source bytes")
			in := suppliedPDFExtractionFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash, mixed)
			resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
			require.Equal(t, http.StatusCreated, resp.StatusCode, body)
			var receipt api.SuppliedOCRPublicationReceipt
			require.NoError(t, json.Unmarshal([]byte(body), &receipt))
			build, err := s.RenditionBuildByID(t.Context(), receipt.BuildID)
			require.NoError(t, err)
			require.Len(t, build.Units, 2)
			for index, unit := range build.Units {
				assert.Equal(t, document.EvidenceLocatorPage, unit.Locator.Kind)
				assert.Equal(t, int64(index+1), unit.Locator.Start)
				assert.Equal(t, int64(index+1), unit.Locator.End)
			}
			var structuredHash string
			for _, artifact := range build.Artifacts {
				if artifact.Role == string(document.EvidenceArtifactStructured) {
					structuredHash = artifact.BlobHash
				}
			}
			require.NotEmpty(t, structuredHash)
			reader, err := s.Blobs.OpenContext(t.Context(), structuredHash)
			require.NoError(t, err)
			retained, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			assert.Equal(t, in.structured, retained)
		})
	}
}

func TestSuppliedOCRPDFExtractionReplayIsExact(t *testing.T) {
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "/source.pdf", "immutable source bytes")
	in := suppliedPDFExtractionFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash, true)

	resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	resp, body = sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Contains(t, body, `"status":"skipped"`)
}

func TestSuppliedOCRPDFExtractionRejectsMalformedWrapper(t *testing.T) {
	ts, s := newTestServer(t, nil)
	node := createFileWithContent(t, ts, s, "/source.pdf", "immutable source bytes")
	in := suppliedPDFExtractionFixtureInput(node.ID, node.CurrentVersionID, node.BlobHash, false)
	in.structured = bytes.Replace(in.structured, []byte(`"page_number":2`), []byte(`"page_number":3`), 1)
	in.structuredLen = int64(len(in.structured))

	resp, body := sendSuppliedOCR(t, ts.URL, ts.Client(), in, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, body)
	assert.Contains(t, body, `"code":"validation"`)
}
