package main

import (
	"fmt"

	"github.com/spf13/cobra"
	docmcp "go.kenn.io/docbank/internal/mcp"
)

var (
	mcpListen          string
	mcpResource        string
	mcpIssuer          string
	mcpJWKSURL         string
	mcpAudience        string
	mcpRequiredScope   string
	mcpAllowedSubjects []string
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the selected vault over authenticated Model Context Protocol",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		auth, err := docmcp.NewAuthenticator(cmd.Context(), docmcp.AuthConfig{
			Resource: mcpResource, Issuer: mcpIssuer, JWKSURL: mcpJWKSURL,
			Audience: mcpAudience, RequiredScope: mcpRequiredScope,
			AllowedSubjects: append([]string(nil), mcpAllowedSubjects...),
		})
		if err != nil {
			return err
		}
		if err := docmcp.ServeHTTP(cmd.Context(), mcpListen, auth, docmcp.NewServer(nil)); err != nil {
			return fmt.Errorf("serving authenticated MCP: %w", err)
		}
		return nil
	},
}

func init() {
	mcpCmd.Flags().StringVar(&mcpListen, "listen", "127.0.0.1:7341", "explicit loopback IP and port")
	mcpCmd.Flags().StringVar(&mcpResource, "oauth-resource", "", "canonical HTTPS protected-resource URL")
	mcpCmd.Flags().StringVar(&mcpIssuer, "oauth-issuer", "", "exact HTTPS access-token issuer")
	mcpCmd.Flags().StringVar(&mcpJWKSURL, "oauth-jwks-url", "", "HTTPS JWKS endpoint")
	mcpCmd.Flags().StringVar(&mcpAudience, "oauth-audience", "", "exact access-token audience")
	mcpCmd.Flags().StringVar(&mcpRequiredScope, "oauth-required-scope", "", "required access-token scope")
	mcpCmd.Flags().StringSliceVar(&mcpAllowedSubjects, "oauth-allowed-subject", nil, "authorized subject (repeatable)")
	rootCmd.AddCommand(mcpCmd)
}
