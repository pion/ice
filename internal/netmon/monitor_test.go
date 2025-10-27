// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package netmon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNetworkMonitor(t *testing.T) {
	// Create a new monitor
	monitor := New()

	// Start monitoring
	err := monitor.Start()
	assert.NoError(t, err, "Failed to start monitor")

	defer func() {
		_ = monitor.Close()
	}()

	// Get initial interfaces
	interfaces, err := monitor.GetInterfaces()
	assert.NoError(t, err, "Failed to get interfaces")

	if len(interfaces) == 0 {
		t.Skip("No network interfaces found")
	}

	// Verify we have at least one interface
	t.Logf("Found %d network interfaces", len(interfaces))
	for _, iface := range interfaces {
		t.Logf(
			"  Interface: %s (index=%d, state=%s, addresses=%d)",
			iface.Name,
			iface.Index,
			iface.State,
			len(iface.Addresses),
		)
	}
}
