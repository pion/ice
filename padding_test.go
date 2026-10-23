// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/require"
)

func TestPaddedCheckSize(t *testing.T) {
	defer test.CheckRoutines(t)()

	stunServerURL := &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   vnetSTUNServerIP,
		Port:   vnetSTUNServerPort,
		Proto:  stun.ProtoTypeUDP,
	}
	natType := &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointIndependent}
	newAgent := func(t *testing.T, n *vnet.Net, paddedCheckSize int) *Agent {
		t.Helper()

		agent, err := NewAgent(&AgentConfig{
			Urls:             []*stun.URI{stunServerURL},
			NetworkTypes:     []NetworkType{NetworkTypeUDP4},
			MulticastDNSMode: MulticastDNSModeDisabled,
			Net:              n,
			PaddedCheckSize:  paddedCheckSize,
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, agent.Close()) })

		return agent
	}

	// The controlling agent sends 1200-byte datagrams, and the path drops datagrams of 1000 bytes
	// or more from the controlled agent.
	for _, tc := range []struct {
		name           string
		controlledSize int
		connects       bool
	}{
		{name: "controlled agent sends small datagrams", controlledSize: 900, connects: true},
		{name: "controlled agent sends large datagrams", controlledSize: 1200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			network, err := buildVNet(natType, natType)
			require.NoError(t, err)
			defer network.close()
			network.wan.AddChunkFilter(func(c vnet.Chunk) bool {
				src, ok := c.SourceAddr().(*net.UDPAddr)

				return !ok || !src.IP.Equal(net.ParseIP(vnetGlobalIPA)) || len(c.UserData()) < 1000
			})

			controlled := newAgent(t, network.net0, tc.controlledSize)
			controlling := newAgent(t, network.net1, 1200)
			gatherAndExchangeCandidates(t, controlled, controlling)

			controlledUfrag, controlledPwd, err := controlled.GetLocalUserCredentials()
			require.NoError(t, err)
			controllingUfrag, controllingPwd, err := controlling.GetLocalUserCredentials()
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			accepted := make(chan error, 1)
			go func() {
				_, err := controlled.Accept(ctx, controllingUfrag, controllingPwd)
				accepted <- err
			}()
			_, dialErr := controlling.Dial(ctx, controlledUfrag, controlledPwd)
			acceptErr := <-accepted

			if tc.connects {
				require.NoError(t, dialErr)
				require.NoError(t, acceptErr)
			} else {
				require.Error(t, dialErr)
				require.Error(t, acceptErr)
			}
		})
	}
}
