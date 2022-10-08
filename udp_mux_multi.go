// Package ice ...
//
//nolint:dupl
package ice

import (
	"net"

	"github.com/pion/logging"
	"github.com/pion/transport/vnet"
)

// MultiUDPMuxDefault implements both UDPMux and AllConnsGetter,
// allowing users to pass multiple UDPMux instances to the ICE agent
// configuration.
type MultiUDPMuxDefault struct {
	muxs           []UDPMux
	localAddrToMux map[string]UDPMux
}

// NewMultiUDPMuxDefault creates an instance of MultiUDPMuxDefault that
// uses the provided UDPMux instances.
func NewMultiUDPMuxDefault(muxs ...UDPMux) *MultiUDPMuxDefault {
	addrToMux := make(map[string]UDPMux)
	for _, mux := range muxs {
		for _, addr := range mux.GetListenAddresses() {
			addrToMux[addr.String()] = mux
		}
	}
	return &MultiUDPMuxDefault{
		muxs:           muxs,
		localAddrToMux: addrToMux,
	}
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
	for _, mux := range m.muxs {
		mux.RemoveConnByUfrag(ufrag)
	}
}

// Close the multi mux, no further connections could be created
func (m *MultiUDPMuxDefault) Close() error {
	var err error
	for _, mux := range m.muxs {
		if e := mux.Close(); e != nil {
			err = e
		}
	}
	return err
}

// GetListenAddresses returns the list of addresses that this mux is listening on
func (m *MultiUDPMuxDefault) GetListenAddresses() []net.Addr {
	addrs := make([]net.Addr, 0, len(m.localAddrToMux))
	for _, mux := range m.muxs {
		addrs = append(addrs, mux.GetListenAddresses()...)
	}
	return addrs
}

// NewMultiUDPMuxFromPort creates an instance of MultiUDPMuxDefault that
// listen all interfaces on the provided port.
func NewMultiUDPMuxFromPort(port int, opts ...UDPMuxFromPortOption) (*MultiUDPMuxDefault, error) {
	params := multiUDPMuxFromPortParam{
		networks: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
	}
	for _, opt := range opts {
		opt.apply(&params)
	}
	muxNet := vnet.NewNet(nil)
	ips, err := localInterfaces(muxNet, params.ifFilter, params.ipFilter, params.networks)
	if err != nil {
		return nil, err
	}

	conns := make([]net.PacketConn, 0, len(ips))
	for _, ip := range ips {
		conn, listenErr := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: port})
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
		conns = append(conns, conn)
	}

	if err != nil {
		for _, conn := range conns {
			_ = conn.Close()
		}
		return nil, err
	}

	muxs := make([]UDPMux, 0, len(conns))
	for _, conn := range conns {
		mux, muxErr := NewUDPMuxDefault(UDPMuxParams{Logger: params.logger, UDPConn: conn})
		if muxErr != nil {
			err = muxErr
			break
		}
		muxs = append(muxs, mux)
	}

	if err != nil {
		for _, mux := range muxs {
			_ = mux.Close()
		}
		return nil, err
	}

	return NewMultiUDPMuxDefault(muxs...), nil
}

// UDPMuxFromPortOption provide options for NewMultiUDPMuxFromPort
type UDPMuxFromPortOption interface {
	apply(*multiUDPMuxFromPortParam)
}

type multiUDPMuxFromPortParam struct {
	ifFilter        func(string) bool
	ipFilter        func(ip net.IP) bool
	networks        []NetworkType
	readBufferSize  int
	writeBufferSize int
	logger          logging.LeveledLogger
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
