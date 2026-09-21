package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/internal/client"
)

func TestServerAdvertisesOnlyReadToolsAndCallsDaemon(t *testing.T) {
	const vaultID = "22222222-2222-4222-8222-222222222222"
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/info":
			_, _ = w.Write([]byte(`{"vault_id":"` + vaultID + `","root_node_id":1}`))
		case "/api/v1/path":
			assert.Equal(t, "/finance", r.URL.Query().Get("path"))
			_, _ = w.Write([]byte(`{"id":42,"name":"finance","kind":"dir","revision":1,"path":"/finance","created_at":"2026-01-01T00:00:00Z","modified_at":"2026-01-01T00:00:00Z"}`))
		case "/api/v1/documents":
			assert.Equal(t, "20", r.URL.Query().Get("limit"))
			under := r.URL.Query().Get("under_node_id")
			if under == "" {
				assert.Empty(t, r.URL.Query().Get("vault_id"))
				_, _ = w.Write([]byte(`{"vault_id":"` + vaultID + `","under_node_id":1,"directory":{"id":1,"name":"","kind":"dir","revision":1,"path":"/","created_at":"2026-01-01T00:00:00Z","modified_at":"2026-01-01T00:00:00Z"},"items":[],"total":0,"limit":20,"offset":0,"next_offset":0,"truncated":false}`))
				return
			}
			assert.Equal(t, "42", under)
			assert.Equal(t, vaultID, r.URL.Query().Get("vault_id"))
			_, _ = w.Write([]byte(`{"vault_id":"` + vaultID + `","under_node_id":42,"directory":{"id":42,"name":"finance","kind":"dir","revision":1,"path":"/finance","created_at":"2026-01-01T00:00:00Z","modified_at":"2026-01-01T00:00:00Z"},"items":[],"total":0,"limit":20,"offset":0,"next_offset":0,"truncated":false}`))
		case "/api/v1/evidence/search":
			assert.Equal(t, "synthetic registration", r.URL.Query().Get("q"))
			assert.Equal(t, "3", r.URL.Query().Get("limit"))
			underField := ""
			path := "/fixture.pdf"
			if r.URL.Query().Get("under_node_id") != "" {
				assert.Equal(t, vaultID, r.URL.Query().Get("vault_id"))
				assert.Equal(t, "42", r.URL.Query().Get("under_node_id"))
				underField = `,"under_node_id":42`
				path = "/finance/fixture.pdf"
			} else {
				assert.Empty(t, r.URL.Query().Get("vault_id"))
			}
			offset := r.URL.Query().Get("offset")
			assert.Contains(t, []string{"0", "7"}, offset)
			offsetValue, _ := strconv.Atoi(offset)
			_, _ = w.Write([]byte(`{"mode":"lexical","vault_id":"` + vaultID + `"` + underField + `,"hits":[{"node":{"id":7,"name":"fixture.pdf","kind":"file","current_version_id":"11111111-1111-4111-8111-111111111111","blob_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42,"revision":1,"created_at":"2026-01-01T00:00:00Z","modified_at":"2026-01-01T00:00:00Z"},"path":"` + path + `","match":"content","evidence_kind":"rendition_segment","build_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","segment_id":"segment-1","excerpt":"synthetic registration"}],"limit":3,"offset":` + strconv.Itoa(offsetValue) + `,"next_offset":` + strconv.Itoa(offsetValue+1) + `,"truncated":false}`))
		default:
			http.NotFound(w, r)
		}
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
		"list_document_versions", "list_documents", "read_rendition_text", "resolve_directory",
		"search_documents"}, names)

	root, err := clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "list_documents",
		Arguments: map[string]any{"limit": 20, "offset": 0}})
	require.NoError(t, err)
	assert.False(t, root.IsError)
	require.Len(t, root.Content, 1)
	rootText, ok := root.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, rootText.Text, `"next_offset":0`)
	assert.Contains(t, rootText.Text, `"truncated":false`)
	invalidScope, err := clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "list_documents",
		Arguments: map[string]any{"under_node_id": 42, "limit": 20}})
	require.NoError(t, err)
	assert.True(t, invalidScope.IsError)

	resolved, err := clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "resolve_directory",
		Arguments: map[string]any{"path": "/finance"}})
	require.NoError(t, err)
	assert.False(t, resolved.IsError)
	require.Len(t, resolved.Content, 1)
	resolvedText, ok := resolved.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, resolvedText.Text, `"vault_id":"`+vaultID+`"`)
	assert.Contains(t, resolvedText.Text, `"id":42`)

	scoped, err := clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "list_documents",
		Arguments: map[string]any{"vault_id": vaultID, "under_node_id": 42, "limit": 20, "offset": 0}})
	require.NoError(t, err)
	assert.False(t, scoped.IsError)
	require.Len(t, scoped.Content, 1)
	scopedText, ok := scoped.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, scopedText.Text, `"next_offset":0`)
	assert.Contains(t, scopedText.Text, `"truncated":false`)

	result, err := clientSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "search_documents",
		Arguments: map[string]any{"query": "synthetic registration", "vault_id": vaultID,
			"under_node_id": 42, "limit": 3, "offset": 7}})
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
