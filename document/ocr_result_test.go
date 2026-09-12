package document_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		"unsupported engine":      func(value *document.SuppliedOCRResult) { value.Engine = "other" },
		"unsupported version":     func(value *document.SuppliedOCRResult) { value.EngineVersion = "0.9.0" },
		"missing source digest":   func(value *document.SuppliedOCRResult) { value.SourceSHA256 = "" },
		"invalid manifest digest": func(value *document.SuppliedOCRResult) { value.ManifestSHA256 = "manifest" },
		"missing manifest size":   func(value *document.SuppliedOCRResult) { value.ManifestBytes = 0 },
		"non-canonical time":      func(value *document.SuppliedOCRResult) { value.ProducedAt = "2026-09-07T12:34:56Z" },
		"blank transcript":        func(value *document.SuppliedOCRResult) { value.Text = " \n\t" },
		"invalid structured JSON": func(value *document.SuppliedOCRResult) { value.Structured = []byte(`{"x":`) },
		"trailing JSON": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Synthetic text","layout":[]} {}`)
		},
		"missing layout": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Synthetic text"}`)
		},
		"missing boxes": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Synthetic text","layout":[{"label":"text"}]}`)
		},
		"duplicate root field": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Synthetic text","markdown":"Synthetic text","layout":[]}`)
		},
		"duplicate nested field": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"schema_version":1,"markdown":"Synthetic text","layout":[{"label":"text","label":"text","boxes":[]}]}`)
		},
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

func TestBuildSuppliedOCREvidenceV1BuildsNativePDFPageEvidence(t *testing.T) {
	policy, err := document.NewEvidencePolicy(1_000)
	require.NoError(t, err)
	text := "Native page one\n\nNative page two"
	raw := []byte(`{"contract_version":"personal-os-pdf-extraction/v1","markdown":"Native page one\n\nNative page two","pages":[{"page_number":1,"source":"native_text","markdown":"Native page one"},{"page_number":2,"source":"native_text","markdown":"Native page two"}]}`)

	evidence, artifacts, err := document.BuildSuppliedOCREvidenceV1(document.SuppliedOCRResult{
		Family: "pdf", Engine: "personal-os-pdf", EngineVersion: "1", Model: "none",
		Recipe: "poppler-native-or-focr-page-v1", ManifestBytes: 0,
		ProducedAt: "2026-09-07T12:34:56.000000000Z", SourceSHA256: strings.Repeat("b", 64),
		Text: text, Structured: raw,
	}, policy)
	require.NoError(t, err)

	assert.Equal(t, document.EvidenceComplete, evidence.Completeness)
	assert.Equal(t, document.EvidenceUnitPage, evidence.UnitKind)
	require.Len(t, evidence.Units, 2)
	for index, unit := range evidence.Units {
		assert.Equal(t, index, unit.Order)
		assert.Equal(t, document.EvidenceLocatorPage, unit.Locator.Kind)
		assert.Equal(t, document.EvidenceIndexOriginOne, unit.Locator.IndexOrigin)
		assert.Equal(t, int64(index+1), unit.Locator.Start)
		assert.Equal(t, int64(index+1), unit.Locator.End)
	}
	require.Len(t, artifacts, 2)
	assert.Equal(t, raw, artifacts[1].Payload)
}

func TestBuildSuppliedOCREvidenceV1PreservesMixedPDFWrapperAndRawFOCR(t *testing.T) {
	policy, err := document.NewEvidencePolicy(1_000)
	require.NoError(t, err)
	focr := "{\n  \"schema_version\": 1, \"markdown\": \"Scanned page two\", \"layout\": []\n}\n"
	text := "Native page one\n\nScanned page two"
	wrapper, err := json.Marshal(map[string]any{
		"contract_version": "personal-os-pdf-extraction/v1", "markdown": text,
		"pages": []any{
			map[string]any{"page_number": 1, "source": "native_text", "markdown": "Native page one"},
			map[string]any{"page_number": 2, "source": "focr", "markdown": "Scanned page two", "focr_json": focr},
		},
	})
	require.NoError(t, err)

	_, artifacts, err := document.BuildSuppliedOCREvidenceV1(document.SuppliedOCRResult{
		Family: "pdf", Engine: "personal-os-pdf", EngineVersion: "1",
		Model: "unlimited-ocr.v0.7.0.int8.focrq", Recipe: "poppler-native-or-focr-page-v1",
		ManifestSHA256: "573340710167697891bf52dfa4cbb5d0a02a68f3011c01f8ef83fd34622fb592",
		ManifestBytes:  4_157_448_783, ProducedAt: "2026-09-07T12:34:56.000000000Z",
		SourceSHA256: strings.Repeat("b", 64), Text: text, Structured: wrapper,
	}, policy)
	require.NoError(t, err)
	assert.Equal(t, wrapper, artifacts[1].Payload)
	var retained struct {
		Pages []struct {
			FOCRJSON string `json:"focr_json"`
		} `json:"pages"`
	}
	require.NoError(t, json.Unmarshal(artifacts[1].Payload, &retained))
	assert.Equal(t, focr, retained.Pages[1].FOCRJSON)
}

func TestBuildSuppliedOCREvidenceV1RejectsMalformedPDFExtraction(t *testing.T) {
	policy, err := document.NewEvidencePolicy(1_000)
	require.NoError(t, err)
	valid := document.SuppliedOCRResult{
		Family: "pdf", Engine: "personal-os-pdf", EngineVersion: "1", Model: "none",
		Recipe: "poppler-native-or-focr-page-v1", ProducedAt: "2026-09-07T12:34:56.000000000Z",
		SourceSHA256: strings.Repeat("b", 64), Text: "Native page one\n\nNative page two",
		Structured: []byte(`{"contract_version":"personal-os-pdf-extraction/v1","markdown":"Native page one\n\nNative page two","pages":[{"page_number":1,"source":"native_text","markdown":"Native page one"},{"page_number":2,"source":"native_text","markdown":"Native page two"}]}`),
	}
	tests := map[string]func(*document.SuppliedOCRResult){
		"wrong contract": func(value *document.SuppliedOCRResult) {
			value.Structured = bytes.Replace(value.Structured, []byte("personal-os-pdf-extraction/v1"), []byte("other/v1"), 1)
		},
		"nonsequential page": func(value *document.SuppliedOCRResult) {
			value.Structured = bytes.Replace(value.Structured, []byte(`"page_number":2`), []byte(`"page_number":3`), 1)
		},
		"unknown page source": func(value *document.SuppliedOCRResult) {
			value.Structured = bytes.Replace(value.Structured, []byte(`"source":"native_text"`), []byte(`"source":"other"`), 1)
		},
		"duplicate wrapper field": func(value *document.SuppliedOCRResult) {
			value.Structured = bytes.Replace(value.Structured, []byte(`{"contract_version"`), []byte(`{"markdown":"duplicate","contract_version"`), 1)
		},
		"native page with focr JSON": func(value *document.SuppliedOCRResult) {
			value.Structured = bytes.Replace(value.Structured, []byte(`"markdown":"Native page one"}`), []byte(`"markdown":"Native page one","focr_json":"{}"}`), 1)
		},
		"transcript reconstruction mismatch": func(value *document.SuppliedOCRResult) { value.Text = "Different" },
		"focr page missing raw JSON": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"contract_version":"personal-os-pdf-extraction/v1","markdown":"Scanned","pages":[{"page_number":1,"source":"focr","markdown":"Scanned"}]}`)
			value.Text = "Scanned"
			setPinnedPDFModel(value)
		},
		"malformed raw focr JSON": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"contract_version":"personal-os-pdf-extraction/v1","markdown":"Scanned","pages":[{"page_number":1,"source":"focr","markdown":"Scanned","focr_json":"not json"}]}`)
			value.Text = "Scanned"
			setPinnedPDFModel(value)
		},
		"raw focr transcript mismatch": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"contract_version":"personal-os-pdf-extraction/v1","markdown":"Scanned","pages":[{"page_number":1,"source":"focr","markdown":"Scanned","focr_json":"{\"schema_version\":1,\"markdown\":\"Different\",\"layout\":[]}"}]}`)
			value.Text = "Scanned"
			setPinnedPDFModel(value)
		},
		"mixed metadata without pinned provenance": func(value *document.SuppliedOCRResult) {
			value.Structured = []byte(`{"contract_version":"personal-os-pdf-extraction/v1","markdown":"Scanned","pages":[{"page_number":1,"source":"focr","markdown":"Scanned","focr_json":"{\"schema_version\":1,\"markdown\":\"Scanned\",\"layout\":[]}"}]}`)
			value.Text = "Scanned"
			value.Model = "unlimited-ocr.v0.7.0.int8.focrq"
		},
		"more than fifty pages": func(value *document.SuppliedOCRResult) {
			pages := make([]map[string]any, 51)
			texts := make([]string, 51)
			for index := range pages {
				texts[index] = fmt.Sprintf("Page %d", index+1)
				pages[index] = map[string]any{"page_number": index + 1, "source": "native_text", "markdown": texts[index]}
			}
			value.Text = strings.Join(texts, "\n\n")
			encoded, err := json.Marshal(map[string]any{
				"contract_version": "personal-os-pdf-extraction/v1", "markdown": value.Text, "pages": pages,
			})
			if err != nil {
				panic(err)
			}
			value.Structured = encoded
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.Structured = append([]byte(nil), valid.Structured...)
			mutate(&input)
			_, _, err := document.BuildSuppliedOCREvidenceV1(input, policy)
			require.Error(t, err)
		})
	}
}

func setPinnedPDFModel(value *document.SuppliedOCRResult) {
	value.Model = "unlimited-ocr.v0.7.0.int8.focrq"
	value.ManifestSHA256 = "573340710167697891bf52dfa4cbb5d0a02a68f3011c01f8ef83fd34622fb592"
	value.ManifestBytes = 4_157_448_783
}
