package document_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
)

func TestBuildSuppliedOCREvidenceV1PreservesProvenanceAndOpaqueResult(t *testing.T) {
	policy, err := document.NewEvidencePolicy(1_000)
	require.NoError(t, err)
	raw := []byte(`{"schema_version":1,"markdown":"Synthetic registration\r\nPlate: TEST-001","layout":[{"label":"text","boxes":[[10,20,300,80]]}]}`)

	evidence, artifacts, err := document.BuildSuppliedOCREvidenceV1(document.SuppliedOCRResult{
		Family:         "image",
		Engine:         "focr",
		EngineVersion:  "0.8.0",
		Model:          "unlimited-ocr.v0.7.0.int8.focrq",
		Recipe:         "unlimited-ocr-ffn-int8-attn-bf16-lmhead-bf16-v1",
		ManifestSHA256: strings.Repeat("a", 64),
		ManifestBytes:  4_157_448_783,
		ProducedAt:     "2026-09-07T12:34:56.000000000Z",
		SourceSHA256:   strings.Repeat("b", 64),
		Text:           "Synthetic registration\r\nPlate: TEST-001",
		Structured:     raw,
	}, policy)
	require.NoError(t, err)

	assert.Equal(t, document.EvidenceDegradedProvenance, evidence.Completeness)
	assert.Equal(t, "image", evidence.Family)
	assert.Equal(t, document.EvidenceUnitGeneric, evidence.UnitKind)
	require.Len(t, evidence.Units, 1)
	assert.Equal(t, "Synthetic registration\nPlate: TEST-001", evidence.Units[0].Text)
	assert.Nil(t, evidence.Units[0].Confidence)
	require.Len(t, artifacts, 2)
	assert.Equal(t, document.EvidenceArtifactTranscript, artifacts[0].Role)
	assert.Equal(t, document.EvidenceArtifactStructured, artifacts[1].Role)
	assert.Equal(t, raw, artifacts[1].Payload)

	var manifest map[string]any
	require.NoError(t, json.Unmarshal(artifacts[0].Payload, &manifest))
	assert.Equal(t, "supplied-ocr/v1", manifest["contract_version"])
	assert.Equal(t, "focr", manifest["engine"])
	assert.Equal(t, "0.8.0", manifest["engine_version"])
	assert.Equal(t, "unlimited-ocr.v0.7.0.int8.focrq", manifest["model"])
	assert.InDelta(t, 4_157_448_783, manifest["manifest_bytes"], 0)
	assert.Equal(t, strings.Repeat("a", 64), manifest["manifest_sha256"])
	assert.Equal(t, strings.Repeat("b", 64), manifest["source_sha256"])
	_, hasConfidence := manifest["confidence"]
	assert.False(t, hasConfidence)
}

func TestBuildSuppliedOCREvidenceV1RejectsUnboundOrInventedInput(t *testing.T) {
	policy, err := document.NewEvidencePolicy(100)
	require.NoError(t, err)
	valid := document.SuppliedOCRResult{
		Family: "pdf", Engine: "focr", EngineVersion: "0.8.0",
		Model:          "unlimited-ocr.v0.7.0.int8.focrq",
		Recipe:         "unlimited-ocr-ffn-int8-attn-bf16-lmhead-bf16-v1",
		ManifestSHA256: strings.Repeat("a", 64),
		ManifestBytes:  4_157_448_783,
		ProducedAt:     "2026-09-07T12:34:56.000000000Z",
		SourceSHA256:   strings.Repeat("b", 64), Text: "Synthetic text",
		Structured: []byte(`{"schema_version":1,"markdown":"Synthetic text","layout":[]}`),
	}

	tests := map[string]func(*document.SuppliedOCRResult){
		"unsupported family":      func(value *document.SuppliedOCRResult) { value.Family = "audio" },
		"missing source digest":   func(value *document.SuppliedOCRResult) { value.SourceSHA256 = "" },
		"invalid manifest digest": func(value *document.SuppliedOCRResult) { value.ManifestSHA256 = "manifest" },
		"missing manifest size":   func(value *document.SuppliedOCRResult) { value.ManifestBytes = 0 },
		"non-canonical time":      func(value *document.SuppliedOCRResult) { value.ProducedAt = "2026-09-07T12:34:56Z" },
		"blank transcript":        func(value *document.SuppliedOCRResult) { value.Text = " \n\t" },
		"invalid structured JSON": func(value *document.SuppliedOCRResult) { value.Structured = []byte(`{"x":`) },
		"schema drift": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":2,"markdown":"Synthetic text","layout":[]}`)
		},
		"unexpected confidence": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Synthetic text","layout":[],"confidence":0.9}`)
		},
		"transcript mismatch": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Different text","layout":[]}`)
		},
		"invalid source box": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Synthetic text","layout":[{"label":"text","boxes":[[30,20,10,80]]}]}`)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			_, _, err := document.BuildSuppliedOCREvidenceV1(input, policy)
			require.Error(t, err)
		})
	}
}

func TestBuildSuppliedOCREvidenceV1DoesNotInterpretStructuredFields(t *testing.T) {
	policy, err := document.NewEvidencePolicy(100)
	require.NoError(t, err)
	raw := []byte(`{"schema_version":1,"markdown":"Plate T3ST-OO1","layout":[{"label":"plate","boxes":[[1,2,30,40]]}]}`)

	evidence, artifacts, err := document.BuildSuppliedOCREvidenceV1(document.SuppliedOCRResult{
		Family: "image", Engine: "focr", EngineVersion: "0.8.0",
		Model:          "unlimited-ocr.v0.7.0.int8.focrq",
		Recipe:         "unlimited-ocr-ffn-int8-attn-bf16-lmhead-bf16-v1",
		ManifestSHA256: strings.Repeat("c", 64),
		ManifestBytes:  4_157_448_783,
		ProducedAt:     "2026-09-07T12:34:56.000000000Z",
		SourceSHA256:   strings.Repeat("d", 64), Text: "Plate T3ST-OO1",
		Structured: raw,
	}, policy)
	require.NoError(t, err)

	assert.Equal(t, "Plate T3ST-OO1", evidence.Units[0].Text)
	assert.Equal(t, raw, artifacts[1].Payload)
	assert.Nil(t, evidence.Units[0].Confidence)
}
