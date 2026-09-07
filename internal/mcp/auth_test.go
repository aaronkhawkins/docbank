package mcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticatorMetadataAndJWTBoundary(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyID := "synthetic-key"
	jwks := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &privateKey.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig",
		}}})
	}))
	defer jwks.Close()

	resource := "https://docbank.example/mcp"
	issuer := "https://auth.example/application/o/docbank-mcp/"
	auth, err := NewAuthenticator(context.Background(), AuthConfig{Resource: resource, Audience: resource,
		Issuer: issuer, JWKSURL: jwks.URL, RequiredScope: "docbank.mcp",
		AllowedSubjects: []string{"owner-subject"}, JWKSHTTPClient: jwks.Client()})
	require.NoError(t, err)
	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	metadataRequest := httptest.NewRequest(http.MethodGet,
		"https://docbank.example/.well-known/oauth-protected-resource", nil)
	metadataResponse := httptest.NewRecorder()
	handler.ServeHTTP(metadataResponse, metadataRequest)
	require.Equal(t, http.StatusOK, metadataResponse.Code)
	assert.Contains(t, metadataResponse.Body.String(), `"resource":"https://docbank.example/mcp"`)
	assert.Contains(t, metadataResponse.Body.String(), `"docbank.mcp"`)

	valid := signToken(t, privateKey, keyID, map[string]any{
		"iss": issuer, "aud": resource, "sub": "owner-subject", "scope": "openid docbank.mcp",
		"iat": time.Now().Add(-time.Minute).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	request := httptest.NewRequest(http.MethodPost, resource, nil)
	request.Header.Set("Authorization", "Bearer "+valid)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code)

	for name, changes := range map[string]map[string]any{
		"wrong audience": {"aud": "https://other.example/mcp"},
		"wrong subject":  {"sub": "someone-else"},
		"missing scope":  {"scope": "openid"},
		"expired":        {"exp": time.Now().Add(-time.Hour).Unix()},
		"future nbf":     {"nbf": time.Now().Add(time.Hour).Unix()},
	} {
		t.Run(name, func(t *testing.T) {
			claims := map[string]any{"iss": issuer, "aud": resource, "sub": "owner-subject",
				"scope": "docbank.mcp", "exp": time.Now().Add(time.Hour).Unix()}
			maps.Copy(claims, changes)
			request := httptest.NewRequest(http.MethodPost, resource, nil)
			request.Header.Set("Authorization", "Bearer "+signToken(t, privateKey, keyID, claims))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.Contains(t, response.Header().Get("WWW-Authenticate"), "resource_metadata=")
			assert.NotContains(t, response.Body.String(), name)
		})
	}

	wrongHost := httptest.NewRequest(http.MethodPost, "https://other.example/mcp", nil)
	wrongHost.Header.Set("Authorization", "Bearer "+valid)
	wrongHostResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongHostResponse, wrongHost)
	assert.Equal(t, http.StatusMisdirectedRequest, wrongHostResponse.Code)
}

func TestAuthenticatorRejectsUnsafeConfigurationAndBearerShapes(t *testing.T) {
	_, err := NewAuthenticator(context.Background(), AuthConfig{Resource: "http://docbank.example/mcp"})
	require.Error(t, err)
	_, err = NewAuthenticator(context.Background(), AuthConfig{Resource: "https://docbank.example/mcp",
		Audience: "https://other.example/mcp", Issuer: "https://auth.example/issuer/",
		JWKSURL: "https://auth.example/jwks/", RequiredScope: "docbank.mcp",
		AllowedSubjects: []string{"owner"}})
	require.ErrorContains(t, err, "audience")

	_, ok := bearerToken([]string{"Bearer one", "Bearer two"})
	assert.False(t, ok)
	_, ok = bearerToken([]string{"Bearer contains space"})
	assert.False(t, ok)
	_, ok = bearerToken([]string{"Basic value"})
	assert.False(t, ok)
}

func signToken(t *testing.T, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256,
		Key: jose.JSONWebKey{Key: key, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"}}, nil)
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	signed, err := signer.Sign(payload)
	require.NoError(t, err)
	compact, err := signed.CompactSerialize()
	require.NoError(t, err)
	return compact
}
