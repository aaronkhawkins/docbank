package processing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"strings"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/canonical"
	"go.kenn.io/docbank/internal/store"
)

const (
	suppliedOCRMaxDocumentChars = 4 << 20
	suppliedOCRMaxUnitRunes     = 4 << 20
	suppliedOCRMaxSegmentRunes  = 4 << 10
)

// SuppliedOCRPublicationInput binds caller-produced focr evidence to one
// immutable Docbank content version. SubmissionKey is the caller's stable
// retry identity; MaterialChecksum is computed independently from its value.
type SuppliedOCRPublicationInput struct {
	VaultID          string
	ContentVersionID string
	SubmissionKey    string
	Result           document.SuppliedOCRResult
	// Preserve exact retries of publications stored before submission-scoped requests.
	LegacyRequestIdentity bool
}

// SuppliedOCRPublicationCandidate is a complete dormant publication plus the
// canonical checksum used to detect a changed request under the same key.
type SuppliedOCRPublicationCandidate struct {
	MaterialChecksum string
	Staged           StagedRendition
}

type suppliedOCRMaterialV1 struct {
	ContractVersion  string `json:"contract_version"`
	ContentVersionID string `json:"content_version_id"`
	Engine           string `json:"engine"`
	EngineVersion    string `json:"engine_version"`
	Family           string `json:"family"`
	ManifestBytes    int64  `json:"manifest_bytes"`
	ManifestSHA256   string `json:"manifest_sha256"`
	Model            string `json:"model"`
	ProducedAt       string `json:"produced_at"`
	Recipe           string `json:"recipe"`
	SourceSHA256     string `json:"source_sha256"`
	StructuredBytes  int64  `json:"structured_bytes"`
	StructuredSHA256 string `json:"structured_sha256"`
	TranscriptBytes  int64  `json:"transcript_bytes"`
	TranscriptSHA256 string `json:"transcript_sha256"`
}

type suppliedOCRProviderReceiptV1 struct {
	ContractVersion  string `json:"contract_version"`
	MaterialChecksum string `json:"material_checksum"`
	SubmissionKey    string `json:"submission_key"`
}

// BuildSuppliedOCRPublicationCandidate validates supplied focr output and
// converts it into the existing verified artifact-publisher contract. It does
// not invoke an OCR provider or mutate catalog/blob authority.
func BuildSuppliedOCRPublicationCandidate(
	input SuppliedOCRPublicationInput,
) (SuppliedOCRPublicationCandidate, error) {
	if !validProcessingSHA256(input.SubmissionKey) {
		return SuppliedOCRPublicationCandidate{}, errors.New(
			"supplied OCR submission key must be canonical lowercase SHA-256")
	}
	evidencePolicy, err := document.NewEvidencePolicy(suppliedOCRMaxDocumentChars)
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}
	evidence, providerArtifacts, err := document.BuildSuppliedOCREvidenceV1(input.Result, evidencePolicy)
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}
	evidenceBytes, evidenceChecksum, err := document.MarshalNormalizedEvidenceV1(evidence)
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}
	renditionPolicy, err := document.NewRenditionPolicy(document.RenditionLimits{
		MaxDocumentChars: suppliedOCRMaxDocumentChars,
		MaxUnitRunes:     suppliedOCRMaxUnitRunes, MaxSegmentRunes: suppliedOCRMaxSegmentRunes,
	})
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}
	rendition, err := document.BuildRenditionV1(evidence, renditionPolicy)
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}
	materialBytes, err := canonical.Marshal(suppliedOCRMaterialV1{
		ContractVersion: "supplied-ocr-material/v1", ContentVersionID: input.ContentVersionID,
		Engine: input.Result.Engine, EngineVersion: input.Result.EngineVersion,
		Family: input.Result.Family, ManifestBytes: input.Result.ManifestBytes,
		ManifestSHA256: input.Result.ManifestSHA256, Model: input.Result.Model,
		ProducedAt: input.Result.ProducedAt, Recipe: input.Result.Recipe,
		SourceSHA256:    input.Result.SourceSHA256,
		StructuredBytes: int64(len(input.Result.Structured)), StructuredSHA256: suppliedOCRSHA256(input.Result.Structured),
		TranscriptBytes: int64(len(input.Result.Text)), TranscriptSHA256: suppliedOCRSHA256([]byte(input.Result.Text)),
	})
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}
	materialChecksum := suppliedOCRSHA256(materialBytes)
	requestIdentity := input.SubmissionKey
	if input.LegacyRequestIdentity {
		requestIdentity = ""
	}
	profile, err := suppliedOCRProcessingProfile(materialChecksum, input.Result.ManifestSHA256, requestIdentity)
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}

	type retainedArtifact struct {
		role    string
		payload []byte
	}
	retained := []retainedArtifact{
		{role: "normalized_evidence", payload: evidenceBytes},
		{role: "sanitized_markdown", payload: rendition.Markdown},
	}
	for _, artifact := range providerArtifacts {
		retained = append(retained, retainedArtifact{role: string(artifact.Role), payload: artifact.Payload})
	}
	buildID := suppliedOCRIdentity("build", input.VaultID, input.SubmissionKey)
	records := make([]store.RenditionArtifactRecord, len(retained))
	payloads := make([]StagedArtifact, len(retained))
	roleCounts := make(map[string]int)
	for index, artifact := range retained {
		hash := suppliedOCRSHA256(artifact.payload)
		ordinal := roleCounts[artifact.role]
		roleCounts[artifact.role]++
		id := renditionArtifactID(buildID, artifact.role, ordinal, hash)
		records[index] = store.RenditionArtifactRecord{
			ID: id, Role: artifact.role, BlobHash: hash, Size: int64(len(artifact.payload)),
			Checksum: hash, State: store.RenditionArtifactVerified,
		}
		payloads[index] = StagedArtifact{ID: id, Payload: bytes.NewReader(artifact.payload)}
	}
	units := make([]store.RenditionUnitRecord, len(rendition.Units))
	for index, unit := range rendition.Units {
		units[index] = store.RenditionUnitRecord{
			ID: unit.ID, EvidenceUnitID: unit.EvidenceUnitID, Order: unit.Order,
			Checksum: unit.Checksum, HeadingPath: append([]string(nil), unit.HeadingPath...), Locator: unit.Locator,
		}
	}
	segments := make([]store.RenditionLexicalSegmentRecord, len(rendition.LexicalSegments))
	for index, segment := range rendition.LexicalSegments {
		segments[index] = store.RenditionLexicalSegmentRecord{
			ID: segment.ID, UnitID: segment.UnitID, Order: segment.Order,
			CharStart: segment.CharStart, CharEnd: segment.CharEnd,
			Checksum: segment.Checksum, Text: segment.Text,
		}
	}
	warnings := make([]string, len(rendition.Warnings))
	for index, warning := range rendition.Warnings {
		warnings[index] = warning.Code
	}
	artifactPolicy := jsontext.Value(`{"roles":[{"max_count":1,"min_count":1,"role":"normalized_evidence"},{"max_count":1,"min_count":1,"role":"provider_transcript"},{"max_count":1,"min_count":1,"role":"sanitized_markdown"},{"max_count":1,"min_count":1,"role":"structured_evidence"}],"version":1}`)
	providerReceipt, err := canonical.Marshal(suppliedOCRProviderReceiptV1{
		ContractVersion:  "supplied-ocr-publication/v1",
		MaterialChecksum: materialChecksum, SubmissionKey: input.SubmissionKey,
	})
	if err != nil {
		return SuppliedOCRPublicationCandidate{}, err
	}
	attachmentID := suppliedOCRIdentity("attachment", buildID, input.ContentVersionID)
	generationID := suppliedOCRIdentity("generation", buildID, input.ContentVersionID)
	attachment := store.RenditionAttachmentRecord{
		ID: attachmentID, VaultID: input.VaultID, ContentVersionID: input.ContentVersionID,
		BuildID: buildID, Profile: profile, AttachedAt: input.Result.ProducedAt,
	}
	build := store.RenditionBuildRecord{
		ID: buildID, VaultID: input.VaultID, SourceSHA256: input.Result.SourceSHA256,
		RenditionRequestFingerprint:       profile.RenditionRequestFingerprint,
		EvidenceLexicalFingerprint:        profile.EvidenceLexicalFingerprint,
		CapturedArtifactPolicyFingerprint: suppliedOCRSHA256(artifactPolicy),
		CapturedArtifactPolicy:            artifactPolicy, AuthorizationChecksum: materialChecksum,
		ProviderOperationID: input.SubmissionKey, ProviderReceipt: jsontext.Value(providerReceipt),
		EvidenceChecksum: evidenceChecksum, RenditionChecksum: rendition.Checksum,
		MarkdownChecksum: rendition.MarkdownChecksum, Completeness: rendition.Completeness,
		Warnings: warnings, CompletedAt: input.Result.ProducedAt,
		DeclaredArtifactCount: len(records), Artifacts: records, Units: units, LexicalSegments: segments,
	}
	return SuppliedOCRPublicationCandidate{MaterialChecksum: materialChecksum, Staged: StagedRendition{
		Rendition: rendition, RenditionPolicy: renditionPolicy, Build: build, Attachment: attachment,
		Head: store.RenditionHeadRecord{
			ContentVersionID: input.ContentVersionID, ProcessingProfileFingerprint: profile.Fingerprint,
			AttachmentID: attachmentID, PublishedAt: input.Result.ProducedAt,
		},
		LexicalGenerationID: generationID, Artifacts: payloads,
	}}, nil
}

func suppliedOCRProcessingProfile(materialChecksum, manifestSHA256, submissionKey string) (store.ProcessingProfileRecord, error) {
	fingerprint := func(subject string) string {
		return suppliedOCRSHA256([]byte("docbank:supplied-ocr-profile:v1\x00" + subject))
	}
	uploadIdentity := "no-upload"
	if submissionKey != "" {
		// A supplied result is a distinct request even when its source bytes match.
		uploadIdentity = "submission:" + submissionKey
	}
	profile := document.ProcessingProfileV1{
		ContractVersion: document.ProcessingProfileContractV1,
		Rendition: &document.RenditionBindingV1{ //nolint:gosec // Stable non-secret profile identity.
			AdapterContract: "supplied-ocr/v1", AuthorizationFingerprint: materialChecksum,
			CredentialBinding:     "credential:supplied-ocr",
			DeploymentFingerprint: manifestSHA256,
			Descriptor:            document.ProviderDescriptorV1{ID: "focr", Fingerprint: fingerprint("focr-0.8.0")},
			DisclosureFingerprint: fingerprint("disclosure"), MaxDocumentBytes: 1 << 40,
			MaxResponseBytes: 64 << 20, MaxUnits: 1, Name: "supplied-ocr",
			RequestedArtifacts: []document.EvidenceArtifactRole{
				document.EvidenceArtifactStructured, document.EvidenceArtifactTranscript,
			},
			TrustBoundary: "caller-supplied-local", UploadOptionsFingerprint: fingerprint(uploadIdentity),
		},
		EvidenceLexical: document.EvidenceLexicalPolicyV1{
			CompletenessFingerprint:     fingerprint("degraded-provenance"),
			LexicalSegmenterFingerprint: fingerprint("lexical-segments"),
			MaxDocumentChars:            suppliedOCRMaxDocumentChars, MaxSegmentRunes: suppliedOCRMaxSegmentRunes,
			MaxUnitRunes:               suppliedOCRMaxUnitRunes,
			NormalizedEvidenceContract: document.NormalizedEvidenceContractV1,
			NormalizerFingerprint:      fingerprint("normalizer"), RenditionContract: document.RenditionContractV1,
			SanitizerFingerprint: fingerprint("sanitizer"), SourceEvidenceContract: document.SourceEvidenceContractV1,
		},
		RetentionDisclosure: document.RetentionDisclosurePolicyV1{
			AttachmentPolicyFingerprint: fingerprint("attachment-policy"),
			ConsentFingerprint:          fingerprint("caller-asserted-consent"),
			RetainSanitizedMarkdown:     true, RetainTypedArtifacts: true,
			TrustBoundary: "caller-supplied-local",
		},
		Retrieval: document.RetrievalPolicyV1{LexicalLimit: 100, VectorLimit: 100},
	}
	encoded, fingerprints, err := document.CanonicalProfile(profile)
	if err != nil {
		return store.ProcessingProfileRecord{}, err
	}
	return store.ProcessingProfileRecord{
		Fingerprint: fingerprints.Profile, CanonicalProfile: jsontext.Value(encoded),
		RenditionRequestFingerprint:    fingerprints.RenditionRequest,
		EvidenceLexicalFingerprint:     fingerprints.EvidenceLexical,
		RetentionDisclosureFingerprint: fingerprints.RetentionDisclosure,
		AttachmentPolicyFingerprint:    profile.RetentionDisclosure.AttachmentPolicyFingerprint,
		ConsentFingerprint:             profile.RetentionDisclosure.ConsentFingerprint,
		RenditionDisclosureFingerprint: profile.Rendition.DisclosureFingerprint,
		TrustBoundary:                  profile.RetentionDisclosure.TrustBoundary,
	}, nil
}

func suppliedOCRIdentity(kind string, values ...string) string {
	var material strings.Builder
	material.WriteString("docbank:supplied-ocr-")
	material.WriteString(kind)
	material.WriteString(":v1")
	for _, value := range values {
		material.WriteByte(0)
		material.WriteString(value)
	}
	return suppliedOCRSHA256([]byte(material.String()))
}

func suppliedOCRSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validProcessingSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
