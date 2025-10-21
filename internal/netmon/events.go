// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package netmon

import (
	"fmt"
	"net/netip"
	"time"
)

// EventType represents the type of network change event.
type EventType int

const (
	// InterfaceAdded indicates a new network interface was added.
	InterfaceAdded EventType = iota
	// InterfaceRemoved indicates a network interface was removed.
	InterfaceRemoved
	// AddressAdded indicates an IP address was added to an interface.
	AddressAdded
	// AddressRemoved indicates an IP address was removed from an interface.
	AddressRemoved
	// StateChanged indicates the operational state of an interface changed.
	StateChanged
	// LinkChanged indicates link-level changes (e.g., cable plugged/unplugged).
	LinkChanged
)

// String returns a string representation of the event type.
func (e EventType) String() string {
	switch e {
	case InterfaceAdded:
		return "InterfaceAdded"
	case InterfaceRemoved:
		return "InterfaceRemoved"
	case AddressAdded:
		return "AddressAdded"
	case AddressRemoved:
		return "AddressRemoved"
	case StateChanged:
		return "StateChanged"
	case LinkChanged:
		return "LinkChanged"
	default:
		return fmt.Sprintf("Unknown(%d)", e)
	}
}

// NetworkEvent represents a network change event.
type NetworkEvent struct {
	Type      EventType        // Type of event
	Interface NetworkInterface // Affected interface
	Timestamp time.Time        // When the event occurred
	Details   map[string]any   // Platform-specific details

	// Additional fields for specific event types
	OldState InterfaceState // Previous state (for StateChanged)
	NewState InterfaceState // New state (for StateChanged)
	Address  netip.Addr     // Affected address (for AddressAdded/Removed)
}

// String returns a string representation of the network event.
func (e NetworkEvent) String() string {
	switch e.Type {
	case InterfaceAdded:
		return fmt.Sprintf("[%s] Interface added: %s", e.Timestamp.Format("15:04:05"), e.Interface.Name)
	case InterfaceRemoved:
		return fmt.Sprintf("[%s] Interface removed: %s", e.Timestamp.Format("15:04:05"), e.Interface.Name)
	case AddressAdded:
		return fmt.Sprintf("[%s] Address added: %s on %s", e.Timestamp.Format("15:04:05"), e.Address, e.Interface.Name)
	case AddressRemoved:
		return fmt.Sprintf("[%s] Address removed: %s from %s", e.Timestamp.Format("15:04:05"), e.Address, e.Interface.Name)
	case StateChanged:
		return fmt.Sprintf(
			"[%s] State changed: %s %s -> %s",
			e.Timestamp.Format("15:04:05"),
			e.Interface.Name,
			e.OldState,
			e.NewState,
		)
	case LinkChanged:
		return fmt.Sprintf("[%s] Link changed: %s", e.Timestamp.Format("15:04:05"), e.Interface.Name)
	default:
		return fmt.Sprintf("[%s] %s: %s", e.Timestamp.Format("15:04:05"), e.Type, e.Interface.Name)
	}
}
