package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateListenAddressRequiresExplicitLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7341", "[::1]:7341", "127.0.0.1:0"} {
		require.NoError(t, ValidateListenAddress(address), address)
	}
	for _, address := range []string{"localhost:7341", "0.0.0.0:7341", "192.0.2.1:7341", ":7341", "127.0.0.1"} {
		assert.Error(t, ValidateListenAddress(address), address)
	}
}
