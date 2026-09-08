package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

const (
	MaxSuppliedOCRMetadataBytes   = int64(64 << 10)
	MaxSuppliedOCRTranscriptBytes = int64(4 << 20)
	MaxSuppliedOCRStructuredBytes = int64(1 << 20)
	suppliedOCRMultipartOverhead  = int64(1 << 20)
)

func registerSuppliedOCRRoute(mux *http.ServeMux, api huma.API, d Deps, g *gate) {
	registerSuppliedOCROpenAPI(api)
	mux.HandleFunc("POST /api/v1/nodes/{id}/supplied-ocr", func(w http.ResponseWriter, r *http.Request) {
		handleSuppliedOCR(w, r, d, g)
	})
}

func registerSuppliedOCROpenAPI(api huma.API) {
	registry := api.OpenAPI().Components.Schemas
	_ = registry.Schema(reflect.TypeFor[SuppliedOCRPublicationMetadata](), true,
		"SuppliedOCRPublicationMetadata")
	receiptSchema := huma.SchemaFromType(registry, reflect.TypeFor[SuppliedOCRPublicationReceipt]())
	errorSchema := huma.SchemaFromType(registry, reflect.TypeFor[Error]())
	response := func(description string) *huma.Response {
		return &huma.Response{Description: description, Content: map[string]*huma.MediaType{
			"application/json": {Schema: receiptSchema},
		}}
	}
	api.OpenAPI().AddOperation(&huma.Operation{
		OperationID: "publishSuppliedOCR", Method: http.MethodPost,
		Path:    "/api/v1/nodes/{id}/supplied-ocr",
		Summary: "Publish caller-supplied focr evidence without executing OCR",
		Description: "Streams exactly three ordered multipart file fields: metadata, transcript, and structured. " +
			"Metadata binds a stable submission key to one node-owned content version, its source SHA-256, " +
			"and declared artifact hashes and sizes.",
		Parameters: []*huma.Param{{Name: "id", In: "path", Required: true,
			Description: "Stable document node ID", Schema: &huma.Schema{Type: "integer", Format: "int64", Minimum: new(float64(1))}}},
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
			"multipart/form-data": {Schema: &huma.Schema{Type: "object", Properties: map[string]*huma.Schema{
				"metadata":   {Type: openAPIStringType, Format: "binary", Description: "SuppliedOCRPublicationMetadata JSON"},
				"transcript": {Type: openAPIStringType, Format: "binary"},
				"structured": {Type: openAPIStringType, Format: "binary"},
			}, Required: []string{"metadata", "transcript", "structured"}}},
		}},
		Responses: map[string]*huma.Response{
			"200": response("Exact retry converged on the existing publication"),
			"201": response("Supplied OCR evidence published"),
			"default": {Description: "Error", Content: map[string]*huma.MediaType{
				"application/problem+json": {Schema: errorSchema},
			}},
		},
	})
}

func handleSuppliedOCR(w http.ResponseWriter, r *http.Request, d Deps, g *gate) {
	nodeID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || nodeID <= 0 {
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation", "node ID must be positive"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxSuppliedOCRMetadataBytes+
		MaxSuppliedOCRTranscriptBytes+MaxSuppliedOCRStructuredBytes+suppliedOCRMultipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, NewError(http.StatusUnsupportedMediaType, "validation",
			"supplied OCR publication requires multipart/form-data: "+err.Error()))
		return
	}
	metadataPart, problem := nextSuppliedOCRPart(reader, "metadata")
	if problem != nil {
		writeError(w, problem)
		return
	}
	metadataBytes, problem := readSuppliedOCRBytes(metadataPart, MaxSuppliedOCRMetadataBytes, "metadata")
	_ = metadataPart.Close()
	if problem != nil {
		writeError(w, problem)
		return
	}
	var metadata SuppliedOCRPublicationMetadata
	if err := json.Unmarshal(metadataBytes, &metadata, json.RejectUnknownMembers(true)); err != nil {
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation", "invalid supplied OCR metadata: "+err.Error()))
		return
	}
	if problem := validateSuppliedOCRMetadata(metadata); problem != nil {
		writeError(w, problem)
		return
	}
	version, err := d.Store.ContentVersionByID(r.Context(), metadata.ContentVersionID)
	if err != nil {
		writeSuppliedOCRStoreError(w, err)
		return
	}
	if version.NodeID != nodeID {
		writeError(w, NewError(http.StatusUnprocessableEntity, "version_node_mismatch",
			"content version does not belong to the requested node"))
		return
	}
	if version.BlobHash != metadata.SourceSHA256 {
		writeError(w, NewError(http.StatusUnprocessableEntity, "source_digest_mismatch",
			"source SHA-256 does not match the immutable content version"))
		return
	}
	transcriptPart, problem := nextSuppliedOCRPart(reader, "transcript")
	if problem != nil {
		writeError(w, problem)
		return
	}
	transcript, problem := readDeclaredSuppliedOCRArtifact(
		transcriptPart, metadata.TranscriptBytes, MaxSuppliedOCRTranscriptBytes,
		metadata.TranscriptSHA256, "transcript")
	_ = transcriptPart.Close()
	if problem != nil {
		writeError(w, problem)
		return
	}
	structuredPart, problem := nextSuppliedOCRPart(reader, "structured")
	if problem != nil {
		writeError(w, problem)
		return
	}
	structured, problem := readDeclaredSuppliedOCRArtifact(
		structuredPart, metadata.StructuredBytes, MaxSuppliedOCRStructuredBytes,
		metadata.StructuredSHA256, "structured")
	_ = structuredPart.Close()
	if problem != nil {
		writeError(w, problem)
		return
	}
	if extra, nextErr := reader.NextPart(); !errors.Is(nextErr, io.EOF) {
		if extra != nil {
			_ = extra.Close()
		}
		writeError(w, NewError(http.StatusUnprocessableEntity, "validation",
			"supplied OCR publication requires exactly three multipart parts"))
		return
	}
	if d.SuppliedOCR == nil {
		writeError(w, NewError(http.StatusServiceUnavailable, "supplied_ocr_unavailable",
			"supplied OCR publication is not configured"))
		return
	}
	var receipt SuppliedOCRPublicationReceipt
	err = g.mutate(func() error {
		var publishErr error
		receipt, publishErr = d.SuppliedOCR.PublishSuppliedOCR(
			r.Context(), nodeID, metadata, transcript, structured)
		return publishErr
	})
	if err != nil {
		if apiErr, ok := errors.AsType[*Error](err); ok {
			writeError(w, apiErr)
			return
		}
		writeSuppliedOCRStoreError(w, err)
		return
	}
	httpStatus := http.StatusCreated
	if receipt.Status == "skipped" {
		httpStatus = http.StatusOK
	}
	writeJSON(w, httpStatus, receipt)
}

func writeSuppliedOCRStoreError(w http.ResponseWriter, err error) {
	problem, ok := errors.AsType[*Error](FromStoreError(err))
	if !ok {
		problem = NewError(http.StatusInternalServerError, "internal", err.Error())
	}
	writeError(w, problem)
}

func nextSuppliedOCRPart(reader *multipart.Reader, expected string) (*multipart.Part, *Error) {
	part, err := reader.NextPart()
	if err != nil {
		return nil, NewError(http.StatusUnprocessableEntity, "validation",
			"missing supplied OCR multipart part "+expected+": "+err.Error())
	}
	if part.FormName() != expected || part.FileName() == "" {
		_ = part.Close()
		return nil, NewError(http.StatusUnprocessableEntity, "validation",
			"supplied OCR multipart parts must be ordered metadata, transcript, structured file fields")
	}
	return part, nil
}

func readSuppliedOCRBytes(part *multipart.Part, limit int64, subject string) ([]byte, *Error) {
	payload, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return nil, NewError(http.StatusUnprocessableEntity, "validation", "reading "+subject+": "+err.Error())
	}
	if int64(len(payload)) > limit {
		return nil, NewError(http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("supplied OCR %s exceeds %d bytes", subject, limit))
	}
	return payload, nil
}

func readDeclaredSuppliedOCRArtifact(
	part *multipart.Part, declaredSize, maximum int64, declaredHash, subject string,
) ([]byte, *Error) {
	if declaredSize <= 0 || declaredSize > maximum {
		return nil, NewError(http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("supplied OCR %s must contain 1-%d bytes", subject, maximum))
	}
	payload, problem := readSuppliedOCRBytes(part, declaredSize, subject)
	if problem != nil {
		return nil, problem
	}
	if int64(len(payload)) != declaredSize {
		return nil, NewError(http.StatusUnprocessableEntity, "size_mismatch",
			fmt.Sprintf("supplied OCR %s size does not match its declaration", subject))
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != declaredHash {
		return nil, NewError(http.StatusUnprocessableEntity, "digest_mismatch",
			fmt.Sprintf("supplied OCR %s SHA-256 does not match its declaration", subject))
	}
	return payload, nil
}

func validateSuppliedOCRMetadata(metadata SuppliedOCRPublicationMetadata) *Error {
	validHash := func(value string) bool {
		decoded, err := hex.DecodeString(value)
		return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
	}
	for subject, value := range map[string]string{
		"source_sha256": metadata.SourceSHA256, "submission_key": metadata.SubmissionKey,
		"manifest_sha256": metadata.ManifestSHA256, "transcript_sha256": metadata.TranscriptSHA256,
		"structured_sha256": metadata.StructuredSHA256,
	} {
		if !validHash(value) {
			return NewError(http.StatusUnprocessableEntity, "validation", subject+" must be canonical lowercase SHA-256")
		}
	}
	if strings.TrimSpace(metadata.ContentVersionID) == "" || metadata.ManifestBytes <= 0 {
		return NewError(http.StatusUnprocessableEntity, "validation",
			"content_version_id and positive manifest_bytes are required")
	}
	if metadata.TranscriptBytes <= 0 || metadata.TranscriptBytes > MaxSuppliedOCRTranscriptBytes ||
		metadata.StructuredBytes <= 0 || metadata.StructuredBytes > MaxSuppliedOCRStructuredBytes {
		return NewError(http.StatusRequestEntityTooLarge, "too_large", "supplied OCR artifact declaration exceeds bounds")
	}
	return nil
}
