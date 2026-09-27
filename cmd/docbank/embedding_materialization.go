package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/processing"
	"go.kenn.io/docbank/internal/store"
)

type renditionEmbeddingScanner struct {
	materializer processing.EmbeddingMaterializer
	binding      document.EmbeddingBindingV1
	descriptor   document.EmbeddingDescriptor
	logger       *slog.Logger
}

func newRenditionEmbeddingScanner(cfg config.Config, catalog *store.Store, blobs *blob.Store,
	runtimes *processing.EmbeddingRuntimeRegistry, logger *slog.Logger,
) (*renditionEmbeddingScanner, error) {
	if !cfg.EmbeddingMaterialization.Enabled {
		return nil, errors.New("embedding materialization is disabled")
	}
	binding, err := cfg.EmbeddingBinding(cfg.EmbeddingMaterialization.Binding)
	if err != nil {
		return nil, err
	}
	for _, descriptor := range runtimes.QueryDescriptors() {
		if descriptor.Fingerprint == binding.Descriptor.Fingerprint && descriptor.ID == binding.Descriptor.ID {
			return &renditionEmbeddingScanner{materializer: processing.EmbeddingMaterializer{
				Catalog: catalog, Blobs: blobs,
			}, binding: binding, descriptor: descriptor, logger: logger}, nil
		}
	}
	return nil, errors.New("embedding materialization runtime is unavailable")
}

func (scanner *renditionEmbeddingScanner) scan(ctx context.Context) {
	report, err := scanner.materializer.ScanCurrentRenditions(ctx, scanner.binding, scanner.descriptor,
		document.RuneTokenizer{}, "operator:docbank-local", "embedding:docbank-local")
	scanner.logger.Info("embedding materialization pass", "examined", report.Examined,
		"queued", report.Queued, "skipped", report.Skipped, "failed", report.Failed,
		"complete", err == nil)
}

func (scanner *renditionEmbeddingScanner) Run(ctx context.Context) error {
	scanner.scan(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			scanner.scan(ctx)
		}
	}
}
