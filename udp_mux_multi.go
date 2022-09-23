// Package ice ...
//
//nolint:dupl
package ice

import "net"

// AllConnsGetter allows multiple fixed UDP or TCP ports to be used,
// each which is multiplexed like UDPMux. AllConnsGetter also acts as
// a UDPMux, in which case it will return a single connection for one
// of the ports.
type AllConnsGetter interface {
	GetAllConns(ufrag string, isIPv6 bool, local net.IP) ([]net.PacketConn, error)
}

// MultiUDPMuxDefault implements both UDPMux and AllConnsGetter,
// allowing users to pass multiple UDPMux instances to the ICE agent
// configuration.
type MultiUDPMuxDefault struct {
	muxs []UDPMux
}

// NewMultiUDPMuxDefault creates an instance of MultiUDPMuxDefault that
// uses the provided UDPMux instances.
func NewMultiUDPMuxDefault(muxs ...UDPMux) *MultiUDPMuxDefault {
	return &MultiUDPMuxDefault{
		muxs: muxs,
	}
}

// GetConn returns a PacketConn given the connection's ufrag and network
// creates the connection if an existing one can't be found. This, unlike
// GetAllConns, will only return a single PacketConn from the first
// mux that was passed in to NewMultiUDPMuxDefault.
func (m *MultiUDPMuxDefault) GetConn(ufrag string, isIPv6 bool, local net.IP) (net.PacketConn, error) {
	// NOTE: We always use the first element here in order to maintain the
	// behavior of using an existing connection if one exists.
	if len(m.muxs) == 0 {
		return nil, errNoUDPMuxAvailable
	}
	return m.muxs[0].GetConn(ufrag, isIPv6, local)
}

// RemoveConnByUfrag stops and removes the muxed packet connection
// from all underlying UDPMux instances.
func (m *MultiUDPMuxDefault) RemoveConnByUfrag(ufrag string) {
	for _, mux := range m.muxs {
		mux.RemoveConnByUfrag(ufrag)
	}
}

// GetAllConns returns a PacketConn for each underlying UDPMux
func (m *MultiUDPMuxDefault) GetAllConns(ufrag string, isIPv6 bool, local net.IP) ([]net.PacketConn, error) {
	if len(m.muxs) == 0 {
		// Make sure that we either return at least one connection or an error.
		return nil, errNoUDPMuxAvailable
	}
	var conns []net.PacketConn
	for _, mux := range m.muxs {
		conn, err := mux.GetConn(ufrag, isIPv6, local)
		if err != nil {
			// For now, this implementation is all or none.
			return nil, err
		}
		if conn != nil {
			conns = append(conns, conn)
		}
	}
	return conns, nil
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
