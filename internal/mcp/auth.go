package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const maxBearerBytes = 16 << 10
const maxJWKSBytes = 1 << 20

// AuthConfig defines one standards-compliant OAuth protected resource. The
// selected Docbank vault is deliberately absent from this identity contract.
type AuthConfig struct {
	Resource        string
	Issuer          string
	JWKSURL         string
	Audience        string
	RequiredScope   string
	AllowedSubjects []string
	JWKSHTTPClient  *http.Client
}

// Authenticator validates Authentik access tokens and serves RFC 9728 metadata.
type Authenticator struct {
	config   AuthConfig
	host     string
	verifier *oidc.IDTokenVerifier
}

func NewAuthenticator(ctx context.Context, config AuthConfig) (*Authenticator, error) {
	if err := validateAuthConfig(config); err != nil {
		return nil, err
	}
	keySet := oidc.NewRemoteKeySet(
		oidc.ClientContext(ctx, boundedJWKSClient(config.JWKSHTTPClient)), config.JWKSURL,
	)
	verifier := oidc.NewVerifier(config.Issuer, keySet, &oidc.Config{
		ClientID: config.Audience, SupportedSigningAlgs: []string{oidc.RS256},
	})
	resource, _ := url.Parse(config.Resource)
	return &Authenticator{config: config, host: resource.Host, verifier: verifier}, nil
}

func boundedJWKSClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	result := *base
	transport := result.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	result.Transport = boundedJWKSTransport{base: transport}
	if result.Timeout == 0 || result.Timeout > 5*time.Second {
		result.Timeout = 5 * time.Second
	}
	result.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("JWKS endpoint must not redirect")
	}
	return &result
}

type boundedJWKSTransport struct{ base http.RoundTripper }

func (transport boundedJWKSTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > maxJWKSBytes {
		_ = response.Body.Close()
		return nil, errors.New("JWKS response exceeds size limit")
	}
	response.Body = &boundedJWKSBody{
		body: response.Body, reader: io.LimitReader(response.Body, maxJWKSBytes+1),
	}
	return response, nil
}

type boundedJWKSBody struct {
	body   io.ReadCloser
	reader io.Reader
	read   int64
}

func (body *boundedJWKSBody) Read(buffer []byte) (int, error) {
	n, err := body.reader.Read(buffer)
	body.read += int64(n)
	if body.read > maxJWKSBytes {
		return 0, errors.New("JWKS response exceeds size limit")
	}
	return n, err
}

func (body *boundedJWKSBody) Close() error { return body.body.Close() }

func validateAuthConfig(config AuthConfig) error {
	for name, value := range map[string]string{"resource": config.Resource, "issuer": config.Issuer,
		"JWKS URL": config.JWKSURL, "audience": config.Audience, "required scope": config.RequiredScope} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("OAuth %s is required", name)
		}
	}
	for _, raw := range []string{config.Resource, config.Issuer, config.JWKSURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("OAuth URL %q must be an absolute HTTPS URL", raw)
		}
	}
	resource, _ := url.Parse(config.Resource)
	if resource.Path != "/mcp" {
		return errors.New("OAuth protected resource path must be /mcp")
	}
	if config.Resource != config.Audience {
		return errors.New("OAuth audience must exactly equal the protected resource")
	}
	if len(config.AllowedSubjects) == 0 || slices.Contains(config.AllowedSubjects, "") {
		return errors.New("OAuth allowed subjects must contain only explicit non-empty identities")
	}
	return nil
}

func (a *Authenticator) Handler(mcpHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	metadata := http.HandlerFunc(a.serveMetadata)
	mux.Handle("GET /.well-known/oauth-protected-resource", metadata)
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp", metadata)
	mux.Handle("/mcp", a.requireResourceHost(a.requireResourceOrigin(a.requireBearer(mcpHandler))))
	return mux
}

func (a *Authenticator) requireResourceHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, a.host) {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireResourceOrigin validates every supplied Origin, including on GET.
// Native MCP clients may omit Origin; browser requests must use the canonical
// public HTTPS origin even though the reverse proxy connects over loopback HTTP.
func (a *Authenticator) requireResourceOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		if len(origins) != 0 && (len(origins) != 1 || !strings.EqualFold(origins[0], "https://"+a.host)) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) serveMetadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	metadata := struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
		ScopesSupported        []string `json:"scopes_supported"`
	}{a.config.Resource, []string{a.config.Issuer}, []string{"header"}, []string{a.config.RequiredScope}}
	if err := json.NewEncoder(w).Encode(metadata); err != nil {
		http.Error(w, "encoding protected resource metadata", http.StatusInternalServerError)
	}
}

func (a *Authenticator) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Values("Authorization"))
		if !ok || a.verify(r.Context(), token) != nil {
			a.unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(values []string) (string, bool) {
	if len(values) != 1 || len(values[0]) > maxBearerBytes || len(values[0]) < len("Bearer ") ||
		!strings.EqualFold(values[0][:len("Bearer ")], "Bearer ") {
		return "", false
	}
	token := values[0][len("Bearer "):]
	return token, token != "" && strings.TrimSpace(token) == token && !strings.ContainsAny(token, " \t\r\n")
}

func (a *Authenticator) verify(ctx context.Context, raw string) error {
	token, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return fmt.Errorf("verifying OAuth token: %w", err)
	}
	var claims struct {
		Subject string `json:"sub"`
		Scope   string `json:"scope"`
	}
	if err := token.Claims(&claims); err != nil {
		return fmt.Errorf("decoding OAuth claims: %w", err)
	}
	if !slices.Contains(a.config.AllowedSubjects, claims.Subject) {
		return errors.New("OAuth subject is not authorized")
	}
	if !slices.Contains(strings.Fields(claims.Scope), a.config.RequiredScope) {
		return errors.New("OAuth token lacks the required scope")
	}
	return nil
}

func (a *Authenticator) unauthorized(w http.ResponseWriter) {
	metadata := strings.TrimSuffix(a.config.Resource, "/mcp") + "/.well-known/oauth-protected-resource"
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, metadata, a.config.RequiredScope))
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
