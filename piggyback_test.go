// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"testing"

	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/require"
)

func TestSped(t *testing.T) {
	defer test.CheckRoutines(t)()

	t.Run("Basic embedding", func(t *testing.T) {
		aNotifier, aConnected := onConnected()
		aAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)
		require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

		var toA string
		fromA := "Hello from A"
		aAgent.SetDtlsCallback(func(packet []byte, rAddr net.Addr) {
			toA = string(packet)
		})
		require.True(t, aAgent.Piggyback([]byte(fromA), true))

		bNotifier, bConnected := onConnected()
		bAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)
		require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

		var toB string
		fromB := "Hello from B"
		bAgent.SetDtlsCallback(func(packet []byte, rAddr net.Addr) {
			toB = string(packet)
		})
		require.True(t, bAgent.Piggyback([]byte(fromB), true))

		gatherAndExchangeCandidates(t, aAgent, bAgent)
		go func() {
			bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
			require.NoError(t, err)
			_, err = aAgent.Accept(context.TODO(), bUfrag, bPwd)
			require.NoError(t, err)
		}()

		go func() {
			aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
			require.NoError(t, err)
			_, err = bAgent.Dial(context.TODO(), aUfrag, aPwd)
			require.NoError(t, err)
		}()

		<-aConnected
		<-bConnected
		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())

		require.Equal(t, toA, fromB)
		require.Equal(t, toB, fromA)
	})
}
