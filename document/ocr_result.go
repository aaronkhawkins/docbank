package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"go.kenn.io/docbank/internal/canonical"
)

const (
	suppliedOCRContractV1      = "supplied-ocr/v1"
	maxSuppliedStructuredBytes = 1 << 20
	suppliedOCRTimeLayout      = "2006-01-02T15:04:05.000000000Z"
)

// SuppliedOCRResult is an already-produced OCR result whose engine and source
// identity are asserted by the caller. Docbank validates the envelope and
// preserves the structured payload, but does not authenticate the engine,
// interpret provider scores, or correct extracted values.
type SuppliedOCRResult struct {
	Family         string
	Engine         string
	EngineVersion  string
	Model          string
	Recipe         string
	ManifestSHA256 string
	ManifestBytes  int64
	ProducedAt     string
	SourceSHA256   string
	Text           string
	Structured     []byte
}

type suppliedOCRArtifactV1 struct {
	ContractVersion string `json:"contract_version"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engine_version"`
	ManifestBytes   int64  `json:"manifest_bytes"`
	ManifestSHA256  string `json:"manifest_sha256"`
	Model           string `json:"model"`
	Recipe          string `json:"recipe"`
	ProducedAt      string `json:"produced_at"`
	SourceSHA256    string `json:"source_sha256"`
	Text            string `json:"text"`
}

type focrOutputV1 struct {
	SchemaVersion int                 `json:"schema_version"`
	Markdown      string              `json:"markdown"`
	Layout        []focrLayoutEntryV1 `json:"layout"`
}

type focrLayoutEntryV1 struct {
	Label string      `json:"label"`
	Boxes [][]float64 `json:"boxes"`
}

// BuildSuppliedOCREvidenceV1 converts a caller-supplied image or PDF OCR
// result into Docbank's existing normalized evidence and retained artifact
// contracts. The transcript manifest binds the exact source and execution
// provenance; Structured remains opaque provider output and is retained
// byte-for-byte. Because this operation receives no verified page geometry,
// it emits a generic unit with an explicit natural-provenance omission.
func BuildSuppliedOCREvidenceV1(
	result SuppliedOCRResult, policy EvidencePolicy,
) (NormalizedEvidenceV1, []RenditionArtifact, error) {
	if err := policy.validate(); err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}
	if result.Family != "image" && result.Family != "pdf" {
		return NormalizedEvidenceV1{}, nil, errors.New("supplied OCR family must be image or pdf")
	}
	if result.Engine != "focr" || result.EngineVersion != "0.8.0" {
		return NormalizedEvidenceV1{}, nil, errors.New(
			"supplied OCR engine must be focr 0.8.0")
	}
	for subject, value := range map[string]string{
		"engine": result.Engine, "engine version": result.EngineVersion,
		"model": result.Model, "recipe": result.Recipe,
	} {
		if err := validateEvidenceIdentifier(value, "supplied OCR "+subject); err != nil {
			return NormalizedEvidenceV1{}, nil, err
		}
	}
	if err := validateSHA256(result.ManifestSHA256, "supplied OCR manifest"); err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}
	if result.ManifestBytes <= 0 {
		return NormalizedEvidenceV1{}, nil, errors.New(
			"supplied OCR manifest byte count must be positive")
	}
	if err := validateSHA256(result.SourceSHA256, "supplied OCR source"); err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}
	producedAt, err := time.Parse(suppliedOCRTimeLayout, result.ProducedAt)
	if err != nil || producedAt.Format(suppliedOCRTimeLayout) != result.ProducedAt {
		return NormalizedEvidenceV1{}, nil, errors.New(
			"supplied OCR produced time must use canonical UTC nanosecond form")
	}
	if err := validateEvidenceText(result.Text, "supplied OCR text"); err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}
	if strings.TrimSpace(result.Text) == "" {
		return NormalizedEvidenceV1{}, nil, errors.New(
			"supplied OCR text must contain non-whitespace text")
	}
	if len(result.Structured) == 0 || len(result.Structured) > maxSuppliedStructuredBytes {
		return NormalizedEvidenceV1{}, nil, errors.New(
			"supplied OCR structured result must be valid JSON of at most 1 MiB")
	}
	if err := validateFOCROutputV1(result.Structured, result.Text); err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}

	manifest, err := canonical.Marshal(suppliedOCRArtifactV1{
		ContractVersion: suppliedOCRContractV1,
		Engine:          result.Engine, EngineVersion: result.EngineVersion,
		ManifestBytes: result.ManifestBytes, ManifestSHA256: result.ManifestSHA256,
		Model: result.Model, Recipe: result.Recipe,
		ProducedAt: result.ProducedAt, SourceSHA256: result.SourceSHA256,
		Text: result.Text,
	})
	if err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}
	manifestSHA256 := digestSHA256(manifest)
	structured := append([]byte(nil), result.Structured...)
	structuredSHA256 := digestSHA256(structured)
	artifacts := []RenditionArtifact{
		{Role: EvidenceArtifactTranscript, MediaType: "application/json", Payload: manifest, SHA256: manifestSHA256},
		{Role: EvidenceArtifactStructured, MediaType: "application/json", Payload: structured, SHA256: structuredSHA256},
	}
	source := SourceEvidenceV1{
		ContractVersion: SourceEvidenceContractV1,
		Completeness:    EvidenceDegradedProvenance,
		Family:          result.Family,
		Omissions: []SourceEvidenceOmissionV1{{
			Field: "natural_provenance", Kind: EvidenceOmissionField,
			Reason: "supplied OCR has no verified page geometry or source-unit provenance",
		}},
		UnitKind: EvidenceUnitGeneric,
		Units: []SourceEvidenceUnitV1{{
			Order: 0, Text: result.Text,
			Locator: SourceEvidenceLocatorV1{
				Kind: EvidenceLocatorGeneric, IndexOrigin: EvidenceIndexOriginNone,
			},
		}},
		Artifacts: []SourceEvidenceArtifactV1{
			{Pointer: "ocr.json", ProviderID: "ocr", Role: EvidenceArtifactTranscript, SHA256: manifestSHA256},
			{Pointer: "structured.json", ProviderID: "structured", Role: EvidenceArtifactStructured, SHA256: structuredSHA256},
		},
	}
	if err := policy.validateSource(source); err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}
	evidence, err := NormalizeEvidenceV1(source, policy)
	if err != nil {
		return NormalizedEvidenceV1{}, nil, err
	}
	return evidence, artifacts, nil
}

func validateFOCROutputV1(encoded []byte, transcript string) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var output focrOutputV1
	if err := decoder.Decode(&output); err != nil {
		return fmt.Errorf("supplied OCR structured result is not focr schema v1: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("supplied OCR structured result contains trailing JSON")
	}
	if output.SchemaVersion != 1 {
		return errors.New("supplied OCR structured result schema version must be 1")
	}
	if output.Markdown != transcript {
		return errors.New("supplied OCR transcript does not match structured result Markdown")
	}
	if output.Layout == nil {
		return errors.New("supplied OCR structured result layout must be present")
	}
	for entryIndex, entry := range output.Layout {
		if err := validateEvidenceText(entry.Label, "supplied OCR layout label"); err != nil ||
			strings.TrimSpace(entry.Label) == "" {
			return fmt.Errorf("supplied OCR layout entry %d has an invalid label", entryIndex)
		}
		if entry.Boxes == nil {
			return fmt.Errorf("supplied OCR layout entry %d boxes must be present", entryIndex)
		}
		for boxIndex, box := range entry.Boxes {
			if len(box) != 4 || !validFOCRBox(box) {
				return fmt.Errorf("supplied OCR layout entry %d box %d is invalid", entryIndex, boxIndex)
			}
		}
	}
	return nil
}

func validFOCRBox(box []float64) bool {
	for _, coordinate := range box {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) || coordinate < 0 {
			return false
		}
	}
	return box[2] >= box[0] && box[3] >= box[1]
}

func validateSHA256(value, subject string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return fmt.Errorf("%s SHA-256 must be 64 lowercase hexadecimal characters", subject)
	}
	return nil
}

func digestSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
