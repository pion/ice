// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package netmon provides event-driven network interface monitoring
// across different operating systems.
package netmon

import (
	"net/netip"
)

// NetworkMonitor provides platform-specific network change monitoring.
type NetworkMonitor interface {
	// Start begins monitoring network changes indefinitely until Close is called
	Start() error

	// Events returns a channel for network change events
	Events() <-chan NetworkEvent

	// GetInterfaces returns current network interfaces
	GetInterfaces() ([]NetworkInterface, error)

	// Close stops monitoring and releases resources
	Close() error
}

// NetworkInterface represents a network interface with its properties.
type NetworkInterface struct {
	Name      string         // Interface name (e.g., "eth0", "en0")
	Index     int            // Interface index
	Addresses []netip.Addr   // IP addresses assigned to this interface
	State     InterfaceState // Current state of the interface
	MTU       int            // Maximum transmission unit
	Flags     uint32         // Interface flags (platform-specific)
	HWAddr    []byte         // Hardware address (MAC)
}

// InterfaceState represents the operational state of a network interface.
type InterfaceState int

const (
	// StateDown indicates the interface is administratively down.
	StateDown InterfaceState = iota
	// StateUp indicates the interface is up and operational.
	StateUp
	// StateUnknown indicates the state cannot be determined.
	StateUnknown
)

// String returns a string representation of the interface state.
func (s InterfaceState) String() string {
	switch s {
	case StateDown:
		return "down"
	case StateUp:
		return "up"
	default:
		return "unknown"
	}
}

// New creates a new NetworkMonitor for the current platform.
func New() NetworkMonitor {
	return newPlatformMonitor()
}
