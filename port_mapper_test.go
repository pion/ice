// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortMapper(t *testing.T) {
	t.Run("Empty mappings", func(t *testing.T) {
		_, err := newPortMapper([]PortMapping{})
		require.NoError(t, err)
	})

	t.Run("Single UDP mapping", func(t *testing.T) {
		mappings := []PortMapping{
			{InternalPort: 3478, ExternalPort: 5000, Protocol: "udp"},
		}
		pm, err := newPortMapper(mappings)
		require.NoError(t, err)
		require.NotNil(t, pm)

		// Test UDP mapping
		require.Equal(t, 5000, pm.getExternalPort(3478, "udp"))

		// Test non-mapped port (should return original)
		require.Equal(t, 3479, pm.getExternalPort(3479, "udp"))

		// Test TCP (no mapping, should return original)
		require.Equal(t, 3478, pm.getExternalPort(3478, "tcp"))
	})

	t.Run("Multiple protocol mappings", func(t *testing.T) {
		mappings := []PortMapping{
			{InternalPort: 3478, ExternalPort: 5000, Protocol: "udp"},
			{InternalPort: 3478, ExternalPort: 5001, Protocol: "tcp"},
			{InternalPort: 3479, ExternalPort: 5002, Protocol: "udp"},
		}
		pm, err := newPortMapper(mappings)
		require.NoError(t, err)
		require.NotNil(t, pm)

		// Test UDP mappings
		require.Equal(t, 5000, pm.getExternalPort(3478, "udp"))
		require.Equal(t, 5002, pm.getExternalPort(3479, "udp"))

		// Test TCP mapping
		require.Equal(t, 5001, pm.getExternalPort(3478, "tcp"))

		// Test unmapped TCP port
		require.Equal(t, 3479, pm.getExternalPort(3479, "tcp"))
	})

	t.Run("Default protocol to TCP", func(t *testing.T) {
		mappings := []PortMapping{
			{InternalPort: 3478, ExternalPort: 5000}, // No protocol specified
		}
		pm, err := newPortMapper(mappings)
		require.NoError(t, err)
		require.NotNil(t, pm)

		// Should default to TCP
		require.Equal(t, 5000, pm.getExternalPort(3478, "tcp"))
		require.Equal(t, 3478, pm.getExternalPort(3478, "udp"))
	})

	t.Run("Invalid protocol error", func(t *testing.T) {
		mappings := []PortMapping{
			{InternalPort: 3478, ExternalPort: 5000, Protocol: "invalid"},
		}
		_, err := newPortMapper(mappings)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported protocol")
	})
}
