package mcp

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/client"
	"go.kenn.io/docbank/internal/version"
)

// ClientFactory returns an ownership-proven client for the one vault selected
// when the MCP process started. Remote identity never influences this choice.
type ClientFactory func(context.Context) (*client.Client, error)

// NewServer builds Docbank's initial read-only MCP surface.
func NewServer(factory ClientFactory) *sdkmcp.Server {
	if factory == nil {
		factory = client.Ensure
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "docbank", Version: version.Version}, nil)

	type searchInput struct {
		Query       string `json:"query" jsonschema:"Lexical query (1-4096 characters)."`
		VaultID     string `json:"vault_id,omitempty" jsonschema:"Vault UUID returned by resolve_directory; required with under_node_id."`
		UnderNodeID int64  `json:"under_node_id,omitempty" jsonschema:"Stable directory node ID returned by resolve_directory; required with vault_id."`
		Limit       int    `json:"limit,omitempty" jsonschema:"Maximum results (1-100, default 20)."`
		Offset      int    `json:"offset,omitempty" jsonschema:"Zero-based result offset."`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "search_documents",
		Description: "Search live documents lexically, optionally below a directory resolved by resolve_directory. Returns stable evidence identities and bounded excerpts; use list_documents to browse without query terms."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in searchInput) (*sdkmcp.CallToolResult, api.EvidenceSearchReport, error) {
			if in.Limit == 0 {
				in.Limit = 20
			}
			if strings.TrimSpace(in.Query) == "" || utf8.RuneCountInString(in.Query) > 4096 ||
				!validScope(in.VaultID, in.UnderNodeID) ||
				in.Limit < 1 || in.Limit > 100 || in.Offset < 0 {
				return invalidToolCall[api.EvidenceSearchReport]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (api.EvidenceSearchReport, error) {
				return c.SearchEvidenceWithOptions(ctx, in.Query, in.Limit, client.EvidenceSearchOptions{
					VaultID: in.VaultID, UnderNodeID: in.UnderNodeID, Offset: in.Offset,
				})
			})
		})

	type directoryInput struct {
		Path string `json:"path" jsonschema:"Absolute live virtual directory path."`
	}
	type resolvedDirectory struct {
		VaultID   string   `json:"vault_id"`
		Directory api.Node `json:"directory"`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "resolve_directory",
		Description: "Resolve an absolute live directory path to vault-qualified stable scope authority for list_documents or search_documents."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in directoryInput) (*sdkmcp.CallToolResult, resolvedDirectory, error) {
			if !strings.HasPrefix(in.Path, "/") {
				return invalidToolCall[resolvedDirectory]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (resolvedDirectory, error) {
				info, err := c.Info(ctx)
				if err != nil {
					return resolvedDirectory{}, err
				}
				directory, err := c.Stat(ctx, in.Path)
				if err != nil {
					return resolvedDirectory{}, err
				}
				if directory.Kind != "dir" || directory.TrashedAt != "" {
					return resolvedDirectory{}, errors.New("resolved node is not a live directory")
				}
				return resolvedDirectory{VaultID: info.VaultID, Directory: directory}, nil
			})
		})

	type documentsInput struct {
		VaultID     string `json:"vault_id,omitempty" jsonschema:"Vault UUID returned by resolve_directory; required with under_node_id."`
		UnderNodeID int64  `json:"under_node_id,omitempty" jsonschema:"Stable directory node ID returned by resolve_directory; required with vault_id."`
		Limit       int    `json:"limit,omitempty" jsonschema:"Page size (1-100, default 20)."`
		Offset      int    `json:"offset,omitempty" jsonschema:"Zero-based page offset."`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "list_documents",
		Description: "Browse current live files recursively at vault root or below a directory resolved by resolve_directory. This is inventory, not lexical search."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in documentsInput) (*sdkmcp.CallToolResult, api.DocumentPage, error) {
			if in.Limit == 0 {
				in.Limit = 20
			}
			if !validScope(in.VaultID, in.UnderNodeID) || in.Limit < 1 || in.Limit > 100 || in.Offset < 0 {
				return invalidToolCall[api.DocumentPage]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (api.DocumentPage, error) {
				return c.Documents(ctx, in.VaultID, in.UnderNodeID, in.Limit, in.Offset)
			})
		})

	type nodeInput struct {
		NodeID int64 `json:"node_id" jsonschema:"Stable positive document node ID."`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "get_document",
		Description: "Inspect current metadata and content authority for one stable document node."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in nodeInput) (*sdkmcp.CallToolResult, api.Node, error) {
			if in.NodeID < 1 {
				return invalidToolCall[api.Node]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (api.Node, error) { return c.Node(ctx, in.NodeID) })
		})

	type pageInput struct {
		NodeID int64 `json:"node_id" jsonschema:"Stable positive document node ID."`
		Limit  int   `json:"limit,omitempty" jsonschema:"Page size (1-100, default 20)."`
		Offset int   `json:"offset,omitempty" jsonschema:"Zero-based page offset."`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "list_document_versions",
		Description: "List immutable content versions for one stable document node, newest first."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in pageInput) (*sdkmcp.CallToolResult, api.ContentVersionPage, error) {
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.NodeID < 1 || in.Limit < 1 || in.Limit > 100 || in.Offset < 0 {
				return invalidToolCall[api.ContentVersionPage]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (api.ContentVersionPage, error) {
				return c.Versions(ctx, in.NodeID, in.Limit, in.Offset)
			})
		})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "get_provenance",
		Description: "List immutable origin facts for one stable document node."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in pageInput) (*sdkmcp.CallToolResult, api.ProvenancePage, error) {
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.NodeID < 1 || in.Limit < 1 || in.Limit > 100 || in.Offset < 0 {
				return invalidToolCall[api.ProvenancePage]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (api.ProvenancePage, error) {
				return c.Provenance(ctx, in.NodeID, in.Limit, in.Offset)
			})
		})

	type referencesInput struct {
		SHA256 string `json:"sha256" jsonschema:"Lowercase SHA-256 content identity."`
		Limit  int    `json:"limit,omitempty" jsonschema:"Page size (1-100, default 20)."`
		Offset int    `json:"offset,omitempty" jsonschema:"Zero-based page offset."`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "find_content_references",
		Description: "Find current, historical, and trashed logical versions that retain an exact content hash."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in referencesInput) (*sdkmcp.CallToolResult, api.ContentReferencePage, error) {
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.Limit < 1 || in.Limit > 100 || in.Offset < 0 {
				return invalidToolCall[api.ContentReferencePage]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (api.ContentReferencePage, error) {
				return c.ContentReferences(ctx, in.SHA256, in.Limit, in.Offset)
			})
		})

	type renditionInput struct {
		BuildID string `json:"build_id" jsonschema:"Immutable rendition build SHA-256."`
		Limit   int    `json:"limit,omitempty" jsonschema:"Segment page size (1-100, default 20)."`
		Offset  int    `json:"offset,omitempty" jsonschema:"Zero-based segment offset."`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "read_rendition_text",
		Description: "Read a bounded page of normalized OCR or transcript text from an exact rendition build."},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in renditionInput) (*sdkmcp.CallToolResult, api.RenditionTextPage, error) {
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.Limit < 1 || in.Limit > 100 || in.Offset < 0 {
				return invalidToolCall[api.RenditionTextPage]()
			}
			return daemonCall(ctx, factory, func(c *client.Client) (api.RenditionTextPage, error) {
				return c.RenditionText(ctx, in.BuildID, in.Limit, in.Offset)
			})
		})
	return server
}

func validScope(vaultID string, underNodeID int64) bool {
	if vaultID == "" && underNodeID == 0 {
		return true
	}
	return client.IsCanonicalUUIDv4(vaultID) && underNodeID > 0
}

func invalidToolCall[T any]() (*sdkmcp.CallToolResult, T, error) {
	var zero T
	return toolError("Invalid Docbank tool arguments"), zero, nil
}

func daemonCall[T any](
	ctx context.Context, factory ClientFactory, call func(*client.Client) (T, error),
) (*sdkmcp.CallToolResult, T, error) {
	var zero T
	c, err := factory(ctx)
	if err != nil {
		return toolError("Docbank daemon is unavailable"), zero, nil
	}
	defer func() { _ = c.Close() }()
	out, err := call(c)
	if err != nil {
		if ctx.Err() != nil {
			return nil, zero, ctx.Err()
		}
		return toolError("Docbank rejected the read request"), zero, nil
	}
	return nil, out, nil
}

func toolError(message string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: message}}}
}
