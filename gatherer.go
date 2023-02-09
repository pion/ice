package ice

import (
	"context"
	"net"

	"github.com/pion/stun"
	"github.com/pion/transport/v2"
	"golang.org/x/net/proxy"
)

type GatherParams struct {
	// URIs is a list of STUN and TURN URIs
	URIs []*stun.URI

	// NetworkTypes is an optional configuration for disabling or enabling
	// support for specific network types.
	NetworkTypes []NetworkType

	// CandidateTypes is an optional configuration for disabling or enabling
	// support for specific candidate types.
	CandidateTypes []CandidateType

	// InterfaceFilter is a function that you can use in order to whitelist or blacklist
	// the interfaces which are used to gather ICE candidates.
	InterfaceFilter func(string) bool

	// IPFilter is a function that you can use in order to whitelist or blacklist
	// the IPs which are used to gather ICE candidates.
	IPFilter func(net.IP) bool

	// Include loopback addresses in the candidate list.
	IncludeLoopback bool

	// InsecureSkipVerify controls if self-signed certificates are accepted when connecting
	// to TURN servers via TLS or DTLS
	InsecureSkipVerify bool

	// NAT1To1IPCandidateType is used along with NAT1To1IPs to specify which candidate type
	// the 1:1 NAT IP addresses should be mapped to.
	// If unspecified or CandidateTypeHost, NAT1To1IPs are used to replace host candidate IPs.
	// If CandidateTypeServerReflexive, it will insert a srflx candidate (as if it was derived
	// from a STUN server) with its port number being the one for the actual host candidate.
	// Other values will result in an error.
	NAT1To1IPCandidateType CandidateType

	// NAT1To1IPs contains a list of public IP addresses that are to be used as a host
	// candidate or srflx candidate. This is used typically for servers that are behind
	// 1:1 D-NAT (e.g. AWS EC2 instances) and to eliminate the need of server reflexive
	// candidate gathering.
	NAT1To1IPs []string

	// MulticastDNSMode controls mDNS behavior for the ICE agent
	MulticastDNSMode MulticastDNSMode

	// MulticastDNSHostName controls the hostname for this agent. If none is specified a random one will be generated
	MulticastDNSHostName string

	// PortMin and PortMax are optional. Leave them 0 for the default UDP port allocation strategy.
	PortMin uint16
	PortMax uint16

	// Proxy Dialer is a dialer that should be implemented by the user based on golang.org/x/net/proxy
	// dial interface in order to support corporate proxies
	ProxyDialer proxy.Dialer

	// TCPMux will be used for multiplexing incoming TCP connections for ICE TCP.
	// Currently only passive candidates are supported. This functionality is
	// experimental and the API might change in the future.
	TCPMux TCPMux

	// UDPMux is used for multiplexing multiple incoming UDP connections on a single port
	// when this is set, the agent ignores PortMin and PortMax configurations and will
	// defer to UDPMux for incoming connections
	UDPMux UDPMux

	Net transport.Net
}

type CandidateFoundHandler func(ctx context.Context, cand Candidate, conn net.PacketConn) error

type Gatherer interface {
	Gather(context.Context, CandidateFoundHandler) error
}

func NewDefaultGatherer(p *GatherParams) Gatherer {
	mg := MultiGatherer{}

	for _, t := range p.CandidateTypes {
		switch t {
		case CandidateTypeHost:
			mg = append(mg, NewHostGatherer(p))

			if p.MulticastDNSMode == MulticastDNSModeQueryAndGather {
				mg = append(mg, &MulticastDNSGatherer{})
			}

		case CandidateTypeServerReflexive:
			mg = append(mg, NewServerReflexiveGatherer(p))

		case CandidateTypeRelay:
			mg = append(mg, NewRelayGatherers(p))
		}
	}

	var g Gatherer = mg

	if p.NAT1To1IPs != nil {
		g, _ = NewMappingGathererInterceptor(p.NAT1To1IPs, p.NAT1To1IPCandidateType, g)
	}

	return g
}
