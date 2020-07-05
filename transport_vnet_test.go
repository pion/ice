// +build !js

package ice

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/transport/test"
	"github.com/pion/transport/vnet"
	"github.com/stretchr/testify/assert"
)

func TestRemoteLocalAddr(t *testing.T) {
	// Check for leaking routines
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 20)
	defer lim.Stop()

	// Agent0 is behind 1:1 NAT
	natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
	// Agent1 is behind 1:1 NAT
	natType1 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}

	v, errVnet := buildVNet(natType0, natType1)
	if !assert.NoError(t, errVnet, "should succeed") {
		return
	}
	defer v.close()

	stunServerURL := &URL{
		Scheme: SchemeTypeSTUN,
		Host:   vnetSTUNServerIP,
		Port:   vnetSTUNServerPort,
		Proto:  ProtoTypeUDP,
	}

	t.Run("Disconnected Returns nil", func(t *testing.T) {
		disconnectedAgent, err := NewAgent(&AgentConfig{})
		assert.NoError(t, err)

		disconnectedConn := Conn{agent: disconnectedAgent}
		assert.Nil(t, disconnectedConn.RemoteAddr())
		assert.Nil(t, disconnectedConn.LocalAddr())

		assert.NoError(t, disconnectedConn.Close())
	})

	t.Run("Remote/Local Pair Match between Agents", func(t *testing.T) {
		ca, cb := pipeWithVNet(v,
			&agentTestConfig{
				urls: []*URL{stunServerURL},
			},
			&agentTestConfig{
				urls: []*URL{stunServerURL},
			},
		)

		aRAddr := ca.RemoteAddr()
		aLAddr := ca.LocalAddr()
		bRAddr := cb.RemoteAddr()
		bLAddr := cb.LocalAddr()

		// Assert that nothing is nil
		assert.NotNil(t, aRAddr)
		assert.NotNil(t, aLAddr)
		assert.NotNil(t, bRAddr)
		assert.NotNil(t, bLAddr)

		// Assert addresses
		assert.Equal(t, aLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPA, bRAddr.(*net.UDPAddr).Port),
		)
		assert.Equal(t, bLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPB, aRAddr.(*net.UDPAddr).Port),
		)
		assert.Equal(t, aRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPB, bLAddr.(*net.UDPAddr).Port),
		)
		assert.Equal(t, bRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPA, aLAddr.(*net.UDPAddr).Port),
		)

		// Close
		assert.NoError(t, ca.Close())
		assert.NoError(t, cb.Close())
	})
}
