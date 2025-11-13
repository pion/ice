// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build darwin || ios
// +build darwin ios

package netmon

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Network -framework Foundation
#import <Network/Network.h>
#import <dispatch/dispatch.h>

typedef void* monitor_t;
typedef void* path_t;

static monitor_t create_path_monitor() {
    nw_path_monitor_t monitor = nw_path_monitor_create();
    return (void*)monitor;
}

static void start_path_monitor(monitor_t mon, void* context) {
    nw_path_monitor_t monitor = (nw_path_monitor_t)mon;
    dispatch_queue_t queue = dispatch_queue_create("com.pion.netmon", DISPATCH_QUEUE_SERIAL);

    nw_path_monitor_set_queue(monitor, queue);

    nw_path_monitor_set_update_handler(monitor, ^(nw_path_t path) {
        extern void pathUpdateCallback(void* context, void* path);
        pathUpdateCallback(context, (void*)path);
    });

    nw_path_monitor_start(monitor);
}

static void stop_path_monitor(monitor_t mon) {
    nw_path_monitor_t monitor = (nw_path_monitor_t)mon;
    nw_path_monitor_cancel(monitor);
}

static int path_get_status(path_t p) {
    nw_path_t path = (nw_path_t)p;
    nw_path_status_t status = nw_path_get_status(path);
    return (int)status;
}
*/
import "C"

// darwinMonitor implements NetworkMonitor using Network Framework
type darwinMonitor struct {
	events     chan NetworkEvent
	interfaces map[int]*NetworkInterface
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	monitor    C.monitor_t
	closeOnce  sync.Once
}

// Path status constants from Network Framework
const (
	pathStatusInvalid     = 0
	pathStatusSatisfied   = 1
	pathStatusUnsatisfied = 2
	pathStatusSatisfiable = 3
)

var darwinMonitorInstance *darwinMonitor

// newPlatformMonitor creates a new Darwin-specific network monitor
func newPlatformMonitor() NetworkMonitor {
	return &darwinMonitor{
		events:     make(chan NetworkEvent, 100),
		interfaces: make(map[int]*NetworkInterface),
	}
}

// Start begins monitoring network changes using Network Framework
func (m *darwinMonitor) Start() error {
	// Initialize current interfaces
	if err := m.refreshInterfaces(); err != nil {
		return err
	}

	// Create internal context for lifecycle management
	m.ctx, m.cancel = context.WithCancel(context.Background())

	// Create path monitor
	m.monitor = C.create_path_monitor()

	// Store instance for callback
	darwinMonitorInstance = m

	// Start monitoring
	C.start_path_monitor(m.monitor, unsafe.Pointer(m))

	// Start polling goroutine as backup
	m.wg.Add(1)
	go m.pollInterfaces()

	return nil
}

// pollInterfaces periodically checks for interface changes as a backup
func (m *darwinMonitor) pollInterfaces() {
	defer m.wg.Done()

	ticker := time.NewTicker(2 * time.Second)
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
func (m *darwinMonitor) checkInterfaceChanges() {
	newInterfaces := make(map[int]*NetworkInterface)

	// Get current interfaces using syscalls
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
func (m *darwinMonitor) Events() <-chan NetworkEvent {
	return m.events
}

// GetInterfaces returns current network interfaces
func (m *darwinMonitor) GetInterfaces() ([]NetworkInterface, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	interfaces := make([]NetworkInterface, 0, len(m.interfaces))
	for _, iface := range m.interfaces {
		interfaces = append(interfaces, *iface)
	}

	return interfaces, nil
}

// Close stops monitoring and releases resources
func (m *darwinMonitor) Close() error {
	m.closeOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}

		if m.monitor != nil {
			C.stop_path_monitor(m.monitor)
		}

		m.wg.Wait()
		close(m.events)
	})

	return nil
}

// refreshInterfaces updates the current interface list
func (m *darwinMonitor) refreshInterfaces() error {
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

// pathUpdateCallback is called from C when network path changes
//
//export pathUpdateCallback
func pathUpdateCallback(context unsafe.Pointer, path unsafe.Pointer) {
	if darwinMonitorInstance != nil {
		status := C.path_get_status(C.path_t(path))

		// Trigger interface check when path changes
		if status == pathStatusSatisfied || status == pathStatusUnsatisfied {
			darwinMonitorInstance.checkInterfaceChanges()
		}
	}
}

// Darwin-specific helper using route sockets as alternative
func listenRouteSocket() (int, error) {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, 0)
	if err != nil {
		return -1, err
	}
	return fd, nil
}
