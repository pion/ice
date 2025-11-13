// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build android
// +build android

package netmon

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// androidMonitor implements NetworkMonitor for Android
// Android inherits Linux netlink capabilities but may have restrictions
// This implementation falls back to polling with /proc/net parsing
type androidMonitor struct {
	events     chan NetworkEvent
	interfaces map[int]*NetworkInterface
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	closeOnce  sync.Once
}

// newPlatformMonitor creates a new Android-specific network monitor
func newPlatformMonitor() NetworkMonitor {
	// Try to use Linux netlink first
	linuxMon := &linuxMonitor{
		events:     make(chan NetworkEvent, 100),
		interfaces: make(map[int]*NetworkInterface),
	}

	// Test if netlink is available
	sock, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err == nil {
		unix.Close(sock)
		// Netlink is available, use Linux implementation
		return linuxMon
	}

	// Fall back to Android-specific implementation
	return &androidMonitor{
		events:     make(chan NetworkEvent, 100),
		interfaces: make(map[int]*NetworkInterface),
	}
}

// Start begins monitoring network changes
func (m *androidMonitor) Start() error {
	// Initialize current interfaces
	if err := m.refreshInterfaces(); err != nil {
		return err
	}

	// Create internal context for lifecycle management
	m.ctx, m.cancel = context.WithCancel(context.Background())

	// Start monitoring goroutine
	m.wg.Add(1)
	go m.monitor()

	return nil
}

// monitor runs the main monitoring loop with polling
func (m *androidMonitor) monitor() {
	defer m.wg.Done()

	// Poll more frequently on Android as we don't have real-time events
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.checkInterfaceChanges()
		}
	}
}

// checkInterfaceChanges compares current interfaces with cached ones
func (m *androidMonitor) checkInterfaceChanges() {
	newInterfaces := make(map[int]*NetworkInterface)

	// Get current interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

	for _, iface := range ifaces {
		// Skip certain Android virtual interfaces
		if shouldSkipAndroidInterface(iface.Name) {
			continue
		}

		state := StateDown
		if iface.Flags&net.FlagUp != 0 {
			state = StateUp
		}

		netIface := &NetworkInterface{
			Name:   iface.Name,
			Index:  iface.Index,
			State:  state,
			MTU:    iface.MTU,
			Flags:  uint32(iface.Flags),
			HWAddr: iface.HardwareAddr,
		}

		// Get addresses
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok {
					if ip, ok := netip.AddrFromSlice(ipnet.IP); ok {
						netIface.Addresses = append(netIface.Addresses, ip)
					}
				}
			}
		}

		newInterfaces[iface.Index] = netIface
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for removed interfaces
	for idx, oldIface := range m.interfaces {
		if _, exists := newInterfaces[idx]; !exists {
			event := NetworkEvent{
				Type:      InterfaceRemoved,
				Interface: *oldIface,
				Timestamp: time.Now(),
			}
			select {
			case m.events <- event:
			default:
			}
		}
	}

	// Check for new or changed interfaces
	for idx, newIface := range newInterfaces {
		oldIface, exists := m.interfaces[idx]
		if !exists {
			// New interface
			event := NetworkEvent{
				Type:      InterfaceAdded,
				Interface: *newIface,
				Timestamp: time.Now(),
			}
			select {
			case m.events <- event:
			default:
			}
		} else {
			// Check for state changes
			if oldIface.State != newIface.State {
				event := NetworkEvent{
					Type:      StateChanged,
					Interface: *newIface,
					OldState:  oldIface.State,
					NewState:  newIface.State,
					Timestamp: time.Now(),
				}
				select {
				case m.events <- event:
				default:
				}
			}

			// Check for address changes
			oldAddrs := make(map[netip.Addr]bool)
			for _, addr := range oldIface.Addresses {
				oldAddrs[addr] = true
			}

			newAddrs := make(map[netip.Addr]bool)
			for _, addr := range newIface.Addresses {
				newAddrs[addr] = true
			}

			// Check for removed addresses
			for addr := range oldAddrs {
				if !newAddrs[addr] {
					event := NetworkEvent{
						Type:      AddressRemoved,
						Interface: *newIface,
						Address:   addr,
						Timestamp: time.Now(),
					}
					select {
					case m.events <- event:
					default:
					}
				}
			}

			// Check for added addresses
			for addr := range newAddrs {
				if !oldAddrs[addr] {
					event := NetworkEvent{
						Type:      AddressAdded,
						Interface: *newIface,
						Address:   addr,
						Timestamp: time.Now(),
					}
					select {
					case m.events <- event:
					default:
					}
				}
			}
		}
	}

	// Update interfaces map
	m.interfaces = newInterfaces
}

// Events returns the event channel
func (m *androidMonitor) Events() <-chan NetworkEvent {
	return m.events
}

// GetInterfaces returns current network interfaces
func (m *androidMonitor) GetInterfaces() ([]NetworkInterface, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	interfaces := make([]NetworkInterface, 0, len(m.interfaces))
	for _, iface := range m.interfaces {
		interfaces = append(interfaces, *iface)
	}

	return interfaces, nil
}

// Close stops monitoring and releases resources
func (m *androidMonitor) Close() error {
	m.closeOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}

		m.wg.Wait()
		close(m.events)
	})

	return nil
}

// refreshInterfaces updates the current interface list
func (m *androidMonitor) refreshInterfaces() error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, iface := range ifaces {
		// Skip certain Android virtual interfaces
		if shouldSkipAndroidInterface(iface.Name) {
			continue
		}

		state := StateDown
		if iface.Flags&net.FlagUp != 0 {
			state = StateUp
		}

		netIface := &NetworkInterface{
			Name:   iface.Name,
			Index:  iface.Index,
			State:  state,
			MTU:    iface.MTU,
			Flags:  uint32(iface.Flags),
			HWAddr: iface.HardwareAddr,
		}

		// Get addresses
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok {
					if ip, ok := netip.AddrFromSlice(ipnet.IP); ok {
						netIface.Addresses = append(netIface.Addresses, ip)
					}
				}
			}
		}

		m.interfaces[iface.Index] = netIface
	}

	return nil
}

// shouldSkipAndroidInterface returns true if the interface should be ignored
func shouldSkipAndroidInterface(name string) bool {
	// Skip common Android virtual interfaces that don't represent real network connectivity
	skipPrefixes := []string{
		"dummy",  // Dummy interfaces
		"tunl",   // Tunnel interfaces
		"sit",    // IPv6-in-IPv4 tunnel
		"ip6tnl", // IPv6 tunnel
		"ip6gre", // IPv6 GRE tunnel
		"teql",   // Traffic equalizer
		"ip_vti", // Virtual tunnel interface
	}

	for _, prefix := range skipPrefixes {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}
