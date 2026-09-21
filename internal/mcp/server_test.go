package mcp

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/client"
)

func TestServerAdvertisesOnlyReadToolsAndCallsDaemon(t *testing.T) {
	const tagID = "11111111-1111-4111-8111-111111111111"
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/evidence/search", r.URL.Path)
		assert.Equal(t, "synthetic registration", r.URL.Query().Get("q"))
		assert.Equal(t, "3", r.URL.Query().Get("limit"))
		assert.Equal(t, tagID, r.URL.Query().Get("tag_id"))
		assert.Equal(t, "application/pdf", r.URL.Query().Get("mime_type"))
		assert.Equal(t, "9", r.URL.Query().Get("under_node_id"))
		assert.Equal(t, "2000-01-01T05:00:00.000000000Z", r.URL.Query().Get("modified_since"))
		assert.Equal(t, "2100-01-01T00:00:00.000000000Z", r.URL.Query().Get("modified_before"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"lexical","hits":[{"node":{"id":7,"name":"fixture.pdf","kind":"file","current_version_id":"11111111-1111-4111-8111-111111111111","blob_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42,"revision":1,"created_at":"2026-01-01T00:00:00Z","modified_at":"2026-01-01T00:00:00Z"},"path":"/fixture.pdf","match":"content","evidence_kind":"rendition_segment","build_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","segment_id":"segment-1","excerpt":"synthetic registration"}],"limit":3,"truncated":false,"tag_id":"11111111-1111-4111-8111-111111111111","mime_type":"application/pdf","under_node_id":9,"modified_since":"2000-01-01T05:00:00.000000000Z","modified_before":"2100-01-01T00:00:00.000000000Z"}`))
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
	var searchSchema []byte
	for _, tool := range tools.Tools {
		if tool.Name == "search_documents" {
			searchSchema, err = json.Marshal(tool.InputSchema)
			require.NoError(t, err)
		}
	}
	require.NotEmpty(t, searchSchema)
	for _, field := range []string{"tag_id", "mime_type", "under_node_id", "modified_since", "modified_before"} {
		assert.Contains(t, string(searchSchema), `"`+field+`"`)
	}

	result, err := clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "search_documents",
		Arguments: map[string]any{
			"query": "synthetic registration", "limit": 3, "tag_id": tagID,
			"mime_type": "APPLICATION/PDF", "under_node_id": 9,
			"modified_since":  "2000-01-01T00:00:00-05:00",
			"modified_before": "2100-01-01T00:00:00Z",
		}})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, `"mode":"lexical"`)
	assert.Contains(t, text.Text, `"build_id":"bbbbbbbb`)
}
