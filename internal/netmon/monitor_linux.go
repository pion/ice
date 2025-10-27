// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build linux
// +build linux

package netmon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// linuxMonitor implements NetworkMonitor using Linux netlink sockets.
type linuxMonitor struct {
	sock       int
	events     chan NetworkEvent
	interfaces map[int]*NetworkInterface
	mu         sync.RWMutex
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	closeOnce  sync.Once
	done       chan struct{}
}

// newPlatformMonitor creates a new Linux-specific network monitor.
func newPlatformMonitor() NetworkMonitor {
	return &linuxMonitor{
		events:     make(chan NetworkEvent, 100),
		interfaces: make(map[int]*NetworkInterface),
		done:       make(chan struct{}),
	}
}

// Start begins monitoring network changes using netlink.
func (m *linuxMonitor) Start() error {
	// Create netlink socket.
	sock, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("failed to create netlink socket: %w", err)
	}
	m.sock = sock

	// Bind to netlink groups for interface and address changes.
	addr := &unix.SockaddrNetlink{
		Family: unix.AF_NETLINK,
		Groups: unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR,
	}
	if err := unix.Bind(sock, addr); err != nil {
		_ = unix.Close(sock)

		return fmt.Errorf("failed to bind netlink socket: %w", err)
	}

	// Set socket to non-blocking mode.
	if err := unix.SetNonblock(sock, true); err != nil {
		_ = unix.Close(sock)

		return fmt.Errorf("failed to set non-blocking: %w", err)
	}

	// Initialize current interfaces.
	if err := m.refreshInterfaces(); err != nil {
		_ = unix.Close(sock)

		return fmt.Errorf("failed to get initial interfaces: %w", err)
	}

	// Create internal context for lifecycle management.
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	// Start monitoring goroutine.
	m.wg.Add(1)
	go m.monitor(ctx)

	return nil
}

// monitor runs the main monitoring loop.
func (m *linuxMonitor) monitor(ctx context.Context) {
	defer m.wg.Done()

	buf := make([]byte, 4096)
	tv := unix.NsecToTimeval(int64(100 * time.Millisecond))

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.done:
			return
		default:
		}

		// Set receive timeout
		_ = unix.SetsockoptTimeval(m.sock, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)

		n, _, err := unix.Recvfrom(m.sock, buf, 0)
		if err != nil {
			m.handleRecvError(ctx, err)

			continue
		}

		// Parse and process netlink messages
		msgs := parseNetlinkMessages(buf[:n])
		for _, msg := range msgs {
			m.processNetlinkMessage(msg)
		}
	}
}

func (m *linuxMonitor) handleRecvError(ctx context.Context, err error) {
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
		return
	}
	// Check if context was canceled.
	select {
	case <-ctx.Done():
	case <-m.done:
	default:
	}
}

// processNetlinkMessage handles individual netlink messages.
func (m *linuxMonitor) processNetlinkMessage(msg *netlinkMessage) {
	switch msg.Header.Type {
	case unix.RTM_NEWLINK:
		m.handleLinkNew(msg)
	case unix.RTM_DELLINK:
		m.handleLinkDel(msg)
	case unix.RTM_NEWADDR:
		m.handleAddrNew(msg)
	case unix.RTM_DELADDR:
		m.handleAddrDel(msg)
	}
}

// handleLinkNew handles new interface events.
func (m *linuxMonitor) handleLinkNew(msg *netlinkMessage) {
	// nolint:gosec // This is intentional netlink message parsing.
	ifinfo := (*ifInfoMsg)(unsafe.Pointer(&msg.Data[0]))
	attrs := parseLinkAttributes(msg.Data[unix.SizeofIfInfomsg:])

	m.mu.Lock()
	defer m.mu.Unlock()

	iface, exists := m.interfaces[int(ifinfo.Index)]
	if !exists {
		m.handleNewInterface(ifinfo, attrs)

		return
	}

	// Check for state change.
	m.handleStateChange(iface, ifinfo)
}

func (m *linuxMonitor) handleNewInterface(ifinfo *ifInfoMsg, attrs map[uint16][]byte) {
	iface := &NetworkInterface{
		Index: int(ifinfo.Index),
		State: ifStateFromFlags(ifinfo.Flags),
		Flags: ifinfo.Flags,
	}

	if name, ok := attrs[unix.IFLA_IFNAME]; ok {
		iface.Name = string(name[:len(name)-1]) // Remove null terminator
	}

	if mtu, ok := attrs[unix.IFLA_MTU]; ok && len(mtu) >= 4 {
		iface.MTU = int(binary.LittleEndian.Uint32(mtu))
	}

	if addr, ok := attrs[unix.IFLA_ADDRESS]; ok {
		iface.HWAddr = addr
	}

	m.interfaces[int(ifinfo.Index)] = iface

	event := NetworkEvent{
		Type:      InterfaceAdded,
		Interface: *iface,
		Timestamp: time.Now(),
	}

	select {
	case m.events <- event:
	default:
	}
}

func (m *linuxMonitor) handleStateChange(iface *NetworkInterface, ifinfo *ifInfoMsg) {
	newState := ifStateFromFlags(ifinfo.Flags)
	if iface.State == newState {
		return
	}

	oldState := iface.State
	iface.State = newState
	iface.Flags = ifinfo.Flags

	event := NetworkEvent{
		Type:      StateChanged,
		Interface: *iface,
		OldState:  oldState,
		NewState:  newState,
		Timestamp: time.Now(),
	}

	select {
	case m.events <- event:
	default:
	}
}

// handleLinkDel handles interface removal events.
func (m *linuxMonitor) handleLinkDel(msg *netlinkMessage) {
	// nolint:gosec // This is intentional netlink message parsing.
	ifinfo := (*ifInfoMsg)(unsafe.Pointer(&msg.Data[0]))

	m.mu.Lock()
	defer m.mu.Unlock()

	if iface, exists := m.interfaces[int(ifinfo.Index)]; exists {
		event := NetworkEvent{
			Type:      InterfaceRemoved,
			Interface: *iface,
			Timestamp: time.Now(),
		}

		delete(m.interfaces, int(ifinfo.Index))

		select {
		case m.events <- event:
		default:
		}
	}
}

// handleAddrNew handles new address events.
func (m *linuxMonitor) handleAddrNew(msg *netlinkMessage) {
	// nolint:gosec // This is intentional netlink message parsing.
	ifaddr := (*ifAddrMsg)(unsafe.Pointer(&msg.Data[0]))
	attrs := parseAddrAttributes(msg.Data[unix.SizeofIfAddrmsg:])

	m.mu.Lock()
	defer m.mu.Unlock()

	iface, exists := m.interfaces[int(ifaddr.Index)]
	if !exists {
		return
	}

	addr := m.extractAddress(attrs, ifaddr.Family)
	if !addr.IsValid() {
		return
	}

	// Check if address already exists.
	for _, existing := range iface.Addresses {
		if existing == addr {
			return
		}
	}

	iface.Addresses = append(iface.Addresses, addr)

	event := NetworkEvent{
		Type:      AddressAdded,
		Interface: *iface,
		Address:   addr,
		Timestamp: time.Now(),
	}

	select {
	case m.events <- event:
	default:
	}
}

func (m *linuxMonitor) extractAddress(attrs map[uint16][]byte, family uint8) netip.Addr {
	addrData, ok := attrs[unix.IFA_LOCAL]
	if !ok || addrData == nil {
		addrData, ok = attrs[unix.IFA_ADDRESS]
		if !ok {
			return netip.Addr{}
		}
	}

	if family == unix.AF_INET && len(addrData) == 4 {
		return netip.AddrFrom4([4]byte(addrData))
	}

	if family == unix.AF_INET6 && len(addrData) == 16 {
		return netip.AddrFrom16([16]byte(addrData))
	}

	return netip.Addr{}
}

// handleAddrDel handles address removal events.
func (m *linuxMonitor) handleAddrDel(msg *netlinkMessage) {
	// nolint:gosec // This is intentional netlink message parsing.
	ifaddr := (*ifAddrMsg)(unsafe.Pointer(&msg.Data[0]))
	attrs := parseAddrAttributes(msg.Data[unix.SizeofIfAddrmsg:])

	m.mu.Lock()
	defer m.mu.Unlock()

	iface, exists := m.interfaces[int(ifaddr.Index)]
	if !exists {
		return
	}

	addrData, ok := attrs[unix.IFA_ADDRESS]
	if !ok {
		return
	}

	addr := m.parseAddrFromData(addrData, ifaddr.Family)
	if !addr.IsValid() {
		return
	}

	// Remove address from interface.
	newAddrs := make([]netip.Addr, 0, len(iface.Addresses))
	for _, existing := range iface.Addresses {
		if existing != addr {
			newAddrs = append(newAddrs, existing)
		}
	}
	iface.Addresses = newAddrs

	event := NetworkEvent{
		Type:      AddressRemoved,
		Interface: *iface,
		Address:   addr,
		Timestamp: time.Now(),
	}

	select {
	case m.events <- event:
	default:
	}
}

func (m *linuxMonitor) parseAddrFromData(addrData []byte, family uint8) netip.Addr {
	if family == unix.AF_INET && len(addrData) == 4 {
		return netip.AddrFrom4([4]byte(addrData))
	}

	if family == unix.AF_INET6 && len(addrData) == 16 {
		return netip.AddrFrom16([16]byte(addrData))
	}

	return netip.Addr{}
}

// Events returns the event channel.
func (m *linuxMonitor) Events() <-chan NetworkEvent {
	return m.events
}

// GetInterfaces returns current network interfaces.
func (m *linuxMonitor) GetInterfaces() ([]NetworkInterface, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	interfaces := make([]NetworkInterface, 0, len(m.interfaces))
	for _, iface := range m.interfaces {
		interfaces = append(interfaces, *iface)
	}

	return interfaces, nil
}

// Close stops monitoring and releases resources.
func (m *linuxMonitor) Close() error {
	var closeErr error

	m.closeOnce.Do(func() {
		close(m.done)

		if m.cancel != nil {
			m.cancel()
		}

		m.wg.Wait()

		if m.sock != 0 {
			closeErr = unix.Close(m.sock)
			m.sock = 0
		}

		close(m.events)
	})

	return closeErr
}

// refreshInterfaces updates the current interface list.
func (m *linuxMonitor) refreshInterfaces() error {
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
			Flags:  uint32(iface.Flags), // nolint:gosec // This is the standard conversion
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

// Helper structures and functions.

// netlinkMessage represents a netlink message.
type netlinkMessage struct {
	Header unix.NlMsghdr
	Data   []byte
}

// ifInfoMsg represents interface information message.
type ifInfoMsg struct {
	Family uint8
	_      uint8
	Type   uint16
	Index  int32
	Flags  uint32
	Change uint32
}

// ifAddrMsg represents interface address message.
type ifAddrMsg struct {
	Family    uint8
	Prefixlen uint8
	Flags     uint8
	Scope     uint8
	Index     uint32
}

// parseNetlinkMessages parses raw netlink data into messages.
func parseNetlinkMessages(data []byte) []*netlinkMessage {
	var msgs []*netlinkMessage

	for len(data) >= unix.SizeofNlMsghdr {
		// nolint:gosec // This is intentional netlink message parsing
		header := (*unix.NlMsghdr)(unsafe.Pointer(&data[0]))

		if header.Len < unix.SizeofNlMsghdr || int(header.Len) > len(data) {
			break
		}

		msg := &netlinkMessage{
			Header: *header,
			Data:   data[unix.SizeofNlMsghdr:header.Len],
		}
		msgs = append(msgs, msg)

		// Align to 4-byte boundary
		msgLen := int(header.Len)
		alignedLen := (msgLen + unix.NLMSG_ALIGNTO - 1) & ^(unix.NLMSG_ALIGNTO - 1)
		data = data[alignedLen:]
	}

	return msgs
}

// parseLinkAttributes parses link attributes from netlink message.
func parseLinkAttributes(data []byte) map[uint16][]byte {
	attrs := make(map[uint16][]byte)

	for len(data) >= 4 {
		// Read attribute header
		attrLen := binary.LittleEndian.Uint16(data[0:2])
		attrType := binary.LittleEndian.Uint16(data[2:4])

		if int(attrLen) < 4 || int(attrLen) > len(data) {
			break
		}

		// Extract attribute data
		if attrLen > 4 {
			attrs[attrType] = data[4:attrLen]
		}

		// Align to 4-byte boundary
		alignedLen := (int(attrLen) + 3) & ^3
		data = data[alignedLen:]
	}

	return attrs
}

// parseAddrAttributes parses address attributes from netlink message.
func parseAddrAttributes(data []byte) map[uint16][]byte {
	return parseLinkAttributes(data) // Same format
}

// ifStateFromFlags converts interface flags to state.
func ifStateFromFlags(flags uint32) InterfaceState {
	if flags&syscall.IFF_UP != 0 {
		return StateUp
	}

	return StateDown
}
