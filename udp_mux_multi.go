// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v2"
	"github.com/pion/transport/v2/stdnet"
	tudp "github.com/pion/transport/v2/udp"
)

var errPortBalanceRequireConnCount = errors.New("Port balance requires UDPMux implements MuxConnCount interface")

// MultiUDPMuxDefault implements both UDPMux and AllConnsGetter,
// allowing users to pass multiple UDPMux instances to the ICE agent
// configuration.
type MultiUDPMuxDefault struct {
	muxes          []UDPMux
	localAddrToMux map[string]UDPMux

	enablePortBalance bool
	// Manage port balance for mux that listen on multiple ports for same IP,
	// for each IP, only return one addr (one port) for each GetListenAddresses call to
	// avoid duplicate ip candidates be gathered for a single ice agent.
	multiPortsAddresses []*multiPortsAddress
}

type addrMux struct {
	addr net.Addr
	mux  MuxConnCount
}

// each multiPortsAddress represents muxes listen on different ports of a same IP
type multiPortsAddress struct {
	addresseMuxes []*addrMux
}

func (mpa *multiPortsAddress) next() net.Addr {
	leastAddr, leastConns := mpa.addresseMuxes[0].addr, mpa.addresseMuxes[0].mux.ConnCount()
	for i := 1; i < len(mpa.addresseMuxes); i++ {
		am := mpa.addresseMuxes[i]
		if count := am.mux.ConnCount(); count < leastConns {
			leastConns = count
			leastAddr = am.addr
		}
	}
	return leastAddr
}

// MultiUDPMuxOption provide options for NewMultiUDPMuxDefault
type MultiUDPMuxOption func(*multipleUDPMuxDefaultParams)

// MultiUDPMuxOptionWithPortBalance enables load balancing traffic on multiple ports belonging to the same IP
// When enabled, GetListenAddresses will return the port with the least number of connections for each corresponding IP
func MultiUDPMuxOptionWithPortBalance() MultiUDPMuxOption {
	return func(params *multipleUDPMuxDefaultParams) {
		params.portBalance = true
	}
}

type multipleUDPMuxDefaultParams struct {
	portBalance bool
}

// NewMultiUDPMuxDefault creates an instance of MultiUDPMuxDefault that
// uses the provided UDPMux instances.
func NewMultiUDPMuxDefault(muxes ...UDPMux) *MultiUDPMuxDefault {
	mux, err := NewMultiUDPMuxDefaultWithOptions(muxes)
	// The error should always be nil as no options given to NewMultiUDPMuxDefaultWithOptions that it
	// only return error when port balance enabled but mux don't support MuxConnCount.
	if err != nil {
		//nolint:forbidigo
		panic(err)
	}
	return mux
}

// NewMultiUDPMuxDefaultWithOptions creates an instance of MultiUDPMuxDefault that
// uses the provided UDPMux instances and options.
func NewMultiUDPMuxDefaultWithOptions(muxes []UDPMux, opts ...MultiUDPMuxOption) (*MultiUDPMuxDefault, error) {
	var params multipleUDPMuxDefaultParams
	for _, opt := range opts {
		opt(&params)
	}

	if params.portBalance {
		for _, mux := range muxes {
			if _, ok := mux.(MuxConnCount); !ok {
				return nil, errPortBalanceRequireConnCount
			}
		}
	}

	addrToMux := make(map[string]UDPMux)
	ipToAddrs := make(map[string]*multiPortsAddress)
	for _, mux := range muxes {
		for _, addr := range mux.GetListenAddresses() {
			addrToMux[addr.String()] = mux

			if params.portBalance {
				muxCount, _ := mux.(MuxConnCount)
				udpAddr, _ := addr.(*net.UDPAddr)
				ip := udpAddr.IP.String()
				if mpa, ok := ipToAddrs[ip]; ok {
					mpa.addresseMuxes = append(mpa.addresseMuxes, &addrMux{addr, muxCount})
				} else {
					ipToAddrs[ip] = &multiPortsAddress{
						addresseMuxes: []*addrMux{{addr, muxCount}},
					}
				}
			}
		}
	}

	multiPortsAddresses := make([]*multiPortsAddress, 0, len(ipToAddrs))
	for _, mpa := range ipToAddrs {
		multiPortsAddresses = append(multiPortsAddresses, mpa)
	}
	return &MultiUDPMuxDefault{
		muxes:               muxes,
		localAddrToMux:      addrToMux,
		multiPortsAddresses: multiPortsAddresses,
		enablePortBalance:   params.portBalance,
	}, nil
}

// GetConn returns a PacketConn given the connection's ufrag and network
// creates the connection if an existing one can't be found.
func (m *MultiUDPMuxDefault) GetConn(ufrag string, addr net.Addr) (net.PacketConn, error) {
	mux, ok := m.localAddrToMux[addr.String()]
	if !ok {
		return nil, errNoUDPMuxAvailable
	}
	return mux.GetConn(ufrag, addr)
}

// RemoveConnByUfrag stops and removes the muxed packet connection
// from all underlying UDPMux instances.
func (m *MultiUDPMuxDefault) RemoveConnByUfrag(ufrag string) {
	for _, mux := range m.muxes {
		mux.RemoveConnByUfrag(ufrag)
	}
}

// Close the multi mux, no further connections could be created
func (m *MultiUDPMuxDefault) Close() error {
	var err error
	for _, mux := range m.muxes {
		if e := mux.Close(); e != nil {
			err = e
		}
	}
	return err
}

// GetListenAddresses returns the list of addresses that this mux is listening on,
// if port balance enabled and there are multiple muxes listening to different ports of the same IP addr,
// it will return the mux that has the least number of connections.
func (m *MultiUDPMuxDefault) GetListenAddresses() []net.Addr {
	if m.enablePortBalance {
		addrs := make([]net.Addr, 0, len(m.multiPortsAddresses))
		for _, mpa := range m.multiPortsAddresses {
			addrs = append(addrs, mpa.next())
		}
		return addrs
	}

	addrs := make([]net.Addr, 0, len(m.localAddrToMux))
	for _, mux := range m.muxes {
		addrs = append(addrs, mux.GetListenAddresses()...)
	}
	return addrs
}

// NewMultiUDPMuxFromPort creates an instance of MultiUDPMuxDefault that
// listen all interfaces on the provided port.
func NewMultiUDPMuxFromPort(port int, opts ...UDPMuxFromPortOption) (*MultiUDPMuxDefault, error) {
	return NewMultiUDPMuxFromPorts([]int{port}, opts...)
}

// NewMultiUDPMuxFromPorts creates an instance of MultiUDPMuxDefault that
// listens to all interfaces and balances traffic on the provided ports.
func NewMultiUDPMuxFromPorts(ports []int, opts ...UDPMuxFromPortOption) (*MultiUDPMuxDefault, error) {
	params := multiUDPMuxFromPortParam{
		networks: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
	}
	for _, opt := range opts {
		opt.apply(&params)
	}

	if params.net == nil {
		var err error
		if params.net, err = stdnet.NewNet(); err != nil {
			return nil, fmt.Errorf("failed to get create network: %w", err)
		}
	}

	ips, err := localInterfaces(params.net, params.ifFilter, params.ipFilter, params.networks, params.includeLoopback)
	if err != nil {
		return nil, err
	}

	conns := make([]net.PacketConn, 0, len(ports)*len(ips))
	for _, ip := range ips {
		for _, port := range ports {
			conn, listenErr := params.net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: port})
			if listenErr != nil {
				err = listenErr
				break
			}
			if params.readBufferSize > 0 {
				_ = conn.SetReadBuffer(params.readBufferSize)
			}
			if params.writeBufferSize > 0 {
				_ = conn.SetWriteBuffer(params.writeBufferSize)
			}
			if params.batchWriteSize > 0 {
				conns = append(conns, tudp.NewBatchConn(conn, params.batchWriteSize, params.batchWriteInterval))
			} else {
				conns = append(conns, conn)
			}
		}
		if err != nil {
			break
		}
	}

	if err != nil {
		for _, conn := range conns {
			_ = conn.Close()
		}
		return nil, err
	}

	muxes := make([]UDPMux, 0, len(conns))
	for _, conn := range conns {
		mux := NewUDPMuxDefault(UDPMuxParams{
			Logger:  params.logger,
			UDPConn: conn,
			Net:     params.net,
		})
		muxes = append(muxes, mux)
	}

	return NewMultiUDPMuxDefaultWithOptions(muxes, MultiUDPMuxOptionWithPortBalance())
}

// UDPMuxFromPortOption provide options for NewMultiUDPMuxFromPort
type UDPMuxFromPortOption interface {
	apply(*multiUDPMuxFromPortParam)
}

type multiUDPMuxFromPortParam struct {
	ifFilter           func(string) bool
	ipFilter           func(ip net.IP) bool
	networks           []NetworkType
	readBufferSize     int
	writeBufferSize    int
	logger             logging.LeveledLogger
	includeLoopback    bool
	net                transport.Net
	batchWriteSize     int
	batchWriteInterval time.Duration
}

type udpMuxFromPortOption struct {
	f func(*multiUDPMuxFromPortParam)
}

func (o *udpMuxFromPortOption) apply(p *multiUDPMuxFromPortParam) {
	o.f(p)
}

// UDPMuxFromPortWithInterfaceFilter set the filter to filter out interfaces that should not be used
func UDPMuxFromPortWithInterfaceFilter(f func(string) bool) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.ifFilter = f
		},
	}
}

// UDPMuxFromPortWithIPFilter set the filter to filter out IP addresses that should not be used
func UDPMuxFromPortWithIPFilter(f func(ip net.IP) bool) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.ipFilter = f
		},
	}
}

// UDPMuxFromPortWithNetworks set the networks that should be used. default is both IPv4 and IPv6
func UDPMuxFromPortWithNetworks(networks ...NetworkType) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.networks = networks
		},
	}
}

// UDPMuxFromPortWithReadBufferSize set the UDP connection read buffer size
func UDPMuxFromPortWithReadBufferSize(size int) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.readBufferSize = size
		},
	}
}

// UDPMuxFromPortWithWriteBufferSize set the UDP connection write buffer size
func UDPMuxFromPortWithWriteBufferSize(size int) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.writeBufferSize = size
		},
	}
}

// UDPMuxFromPortWithLogger set the logger for the created UDPMux
func UDPMuxFromPortWithLogger(logger logging.LeveledLogger) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.logger = logger
		},
	}
}

// UDPMuxFromPortWithLoopback set loopback interface should be included
func UDPMuxFromPortWithLoopback() UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.includeLoopback = true
		},
	}
}

// UDPMuxFromPortWithNet sets the network transport to use.
func UDPMuxFromPortWithNet(n transport.Net) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.net = n
		},
	}
}

// UDPMuxFromPortWithBatchWrite enable batch write for UDPMux
func UDPMuxFromPortWithBatchWrite(batchWriteSize int, batchWriteInterval time.Duration) UDPMuxFromPortOption {
	return &udpMuxFromPortOption{
		f: func(p *multiUDPMuxFromPortParam) {
			p.batchWriteSize = batchWriteSize
			p.batchWriteInterval = batchWriteInterval
		},
	}
}
