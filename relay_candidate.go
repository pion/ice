// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"net"
)

// RelayCandidate is a locally allocated relay candidate and the packet
// connection used to reach the relay. The connection must preserve packet
// boundaries and return the remote candidate address from ReadFrom.
type RelayCandidate struct {
	Config CandidateRelayConfig
	Conn   net.PacketConn
}

// RelayCandidateProvider allocates non-TURN relay candidates. The provider is
// called during candidate gathering when relay candidates are enabled. A
// provider may use the ICE credentials to bind the allocation to this agent,
// but it must not assume that the credentials are the relay authentication
// credentials.
type RelayCandidateProvider interface {
	GatherCandidates(context.Context, string, string) ([]RelayCandidate, error)
}
