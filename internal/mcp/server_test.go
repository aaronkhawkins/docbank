package mcp

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/client"
)

func TestServerAdvertisesOnlyReadToolsAndCallsDaemon(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/evidence/search", r.URL.Path)
		assert.Equal(t, "synthetic registration", r.URL.Query().Get("q"))
		assert.Equal(t, "3", r.URL.Query().Get("limit"))
		offset := r.URL.Query().Get("offset")
		assert.Contains(t, []string{"0", "7"}, offset)
		offsetValue, _ := strconv.Atoi(offset)
		_ = json.MarshalWrite(w, api.EvidenceSearchReport{
			Mode: "lexical",
			Hits: []api.EvidenceSearchHit{{
				Node: api.Node{
					ID: 7, Name: "fixture.pdf", Kind: "file",
					CurrentVersionID: "11111111-1111-4111-8111-111111111111",
					BlobHash:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Size:             42, Revision: 1, CreatedAt: "2026-01-01T00:00:00Z",
					ModifiedAt: "2026-01-01T00:00:00Z",
				},
				Path: "/fixture.pdf", Match: "content", EvidenceKind: "rendition_segment",
				BuildID:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				SegmentID: "segment-1", Excerpt: "synthetic registration",
			}},
			Limit: 3, Offset: offsetValue, NextOffset: offsetValue + 1,
		})
	}))
	defer daemon.Close()

	server := NewServer(func(context.Context) (*client.Client, error) {
		return client.New(daemon.URL, "synthetic-api-key"), nil
	})
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer func() { _ = serverSession.Close() }()
	protocolClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "synthetic-test", Version: "1"}, nil)
	clientSession, err := protocolClient.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	defer func() { _ = clientSession.Close() }()

	tools, err := clientSession.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	assert.Equal(t, []string{"find_content_references", "get_document", "get_provenance",
		"list_document_versions", "read_rendition_text", "search_documents"}, names)

	result, err := clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "search_documents",
		Arguments: map[string]any{"query": "synthetic registration", "limit": 3, "offset": 7}})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, `"mode":"lexical"`)
	assert.Contains(t, text.Text, `"build_id":"bbbbbbbb`)
	assert.Contains(t, text.Text, `"offset":7`)
	assert.Contains(t, text.Text, `"next_offset":8`)

	result, err = clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "search_documents",
		Arguments: map[string]any{"query": "synthetic registration", "limit": 3}})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok = result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, `"offset":0`)
	assert.Contains(t, text.Text, `"next_offset":1`)

	result, err = clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "search_documents",
		Arguments: map[string]any{"query": "synthetic registration", "limit": 3, "offset": -1}})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok = result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Invalid Docbank tool arguments", text.Text)
}
