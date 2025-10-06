// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build windows
// +build windows

package netmon

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsMonitor implements NetworkMonitor using Windows APIs
type windowsMonitor struct {
	events       chan NetworkEvent
	interfaces   map[int]*NetworkInterface
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	overlapped   *windows.Overlapped
	changeHandle windows.Handle
	closeOnce    sync.Once
}

// newPlatformMonitor creates a new Windows-specific network monitor
func newPlatformMonitor() NetworkMonitor {
	return &windowsMonitor{
		events:     make(chan NetworkEvent, 100),
		interfaces: make(map[int]*NetworkInterface),
	}
}

// Start begins monitoring network changes using Windows APIs
func (m *windowsMonitor) Start() error {
	// Initialize current interfaces
	if err := m.refreshInterfaces(); err != nil {
		return err
	}

	// Create internal context for lifecycle management
	m.ctx, m.cancel = context.WithCancel(context.Background())

	// Create overlapped structure for async operations
	m.overlapped = &windows.Overlapped{}
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return err
	}
	m.overlapped.HEvent = event

	// Start monitoring goroutine
	m.wg.Add(1)
	go m.monitor()

	return nil
}

// monitor runs the main monitoring loop
func (m *windowsMonitor) monitor() {
	defer m.wg.Done()
	defer windows.CloseHandle(m.overlapped.HEvent)

	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		// Register for address change notifications
		var handle windows.Handle
		err := notifyAddrChange(&handle, m.overlapped)
		if err != nil && err != windows.ERROR_IO_PENDING {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Wait for notification or timeout
		waitResult, err := windows.WaitForSingleObject(m.overlapped.HEvent, 100)
		if err != nil {
			continue
		}

		if waitResult == windows.WAIT_OBJECT_0 {
			// Network change detected
			m.handleNetworkChange()
		}
	}
}

// handleNetworkChange processes network change events
func (m *windowsMonitor) handleNetworkChange() {
	newInterfaces := make(map[int]*NetworkInterface)

	// Get current interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

	for _, iface := range ifaces {
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
func (m *windowsMonitor) Events() <-chan NetworkEvent {
	return m.events
}

// GetInterfaces returns current network interfaces
func (m *windowsMonitor) GetInterfaces() ([]NetworkInterface, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	interfaces := make([]NetworkInterface, 0, len(m.interfaces))
	for _, iface := range m.interfaces {
		interfaces = append(interfaces, *iface)
	}

	return interfaces, nil
}

// Close stops monitoring and releases resources
func (m *windowsMonitor) Close() error {
	m.closeOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}

		m.wg.Wait()

		if m.changeHandle != 0 {
			windows.CloseHandle(m.changeHandle)
		}

		close(m.events)
	})

	return nil
}

// refreshInterfaces updates the current interface list
func (m *windowsMonitor) refreshInterfaces() error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, iface := range ifaces {
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

// Windows-specific functions

var (
	iphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procNotifyAddrChange = iphlpapi.NewProc("NotifyAddrChange")
)

// notifyAddrChange registers for address change notifications
func notifyAddrChange(handle *windows.Handle, overlapped *windows.Overlapped) error {
	r1, _, err := syscall.SyscallN(
		procNotifyAddrChange.Addr(),
		uintptr(unsafe.Pointer(handle)),
		uintptr(unsafe.Pointer(overlapped)),
	)
	if r1 != 0 {
		if err != windows.ERROR_IO_PENDING {
			return err
		}
	}
	return nil
}
