package ice

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/stretchr/testify/require"
)

func connect_2(aAgent, bAgent *Agent) (*Conn, *Conn) {

	fmt.Println("1")
	gatherAndExchangeCandidates(aAgent, bAgent)
	fmt.Println("2")

	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		bUfrag, bPwd, acceptErr := bAgent.GetLocalUserCredentials()
		fmt.Println("3")

		check(acceptErr)
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		fmt.Println("4")

		check(acceptErr)
		close(accepted)
		fmt.Println("5")

	}()
	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	fmt.Println("6")

	check(err)
	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	check(err)
	fmt.Println("7")

	// Ensure accepted
	<-accepted
	fmt.Println("8")

	return aConn, bConn
}

func TestActiveTCP(t *testing.T) {
	r := require.New(t)

	const port = 7686

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IPv4(192, 168, 1, 92),
		Port: port,
	})
	r.NoError(err)
	defer func() {
		_ = listener.Close()
	}()

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelTrace)

	tcpMux := NewTCPMuxDefault(TCPMuxParams{
		Listener:       listener,
		Logger:         loggerFactory.NewLogger("passive-ice"),
		ReadBufferSize: 20,
	})

	defer func() {
		_ = tcpMux.Close()
	}()

	r.NotNil(tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")
	fmt.Println(tcpMux.LocalAddr())

	ipFilter := func(ip net.IP) bool {
		println("-------------------", ip.To16().String())
		// panic(1)
		return true
	}

	passiveAgent, err := NewAgent(&AgentConfig{
		TCPMux:         tcpMux,
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeTCP4},
		LoggerFactory:  loggerFactory,
		activeTCP:      false,
		IPFilter:       ipFilter,
	})
	r.NoError(err)
	r.NotNil(passiveAgent)

	activeAgent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeTCP4},
		LoggerFactory:  loggerFactory,
		activeTCP:      true,
	})
	r.NoError(err)
	r.NotNil(activeAgent)

	// gatherAndExchangeCandidates(activeAgent, passiveAgent)

	passiveAgentConn, activeAgenConn := connect_2(passiveAgent, activeAgent)
	r.NotNil(passiveAgentConn)
	r.NotNil(activeAgenConn)

	// pair := passiveAgent.getSelectedPair()
	// r.NotNil(pair)
	// r.Equal(port, pair.Local.Port())

	// // send a packet from mux
	// data := []byte("hello world")
	// _, err = passiveAgentConn.Write(data)
	// r.NoError(err)

	// buffer := make([]byte, 1024)
	// n, err := activeAgenConn.Read(buffer)
	// r.NoError(err)
	// r.Equal(data, buffer[:n])

	// // send a packet to mux
	// data2 := []byte("hello world 2")
	// _, err = activeAgenConn.Write(data2)
	// r.NoError(err)

	// n, err = passiveAgentConn.Read(buffer)
	// r.NoError(err)
	// r.Equal(data2, buffer[:n])

	// r.NoError(activeAgenConn.Close())
	// r.NoError(passiveAgentConn.Close())
	// r.NoError(tcpMux.Close())
}

func TestUDP2(t *testing.T) {
	r := require.New(t)

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelTrace)

	passiveAgent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeUDP4},
		LoggerFactory:  loggerFactory,
	})
	r.NoError(err)
	r.NotNil(passiveAgent)

	activeAgent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeUDP4},
		LoggerFactory:  loggerFactory,
	})
	r.NoError(err)
	r.NotNil(activeAgent)

	passiveAgentConn, activeAgenConn := connect(passiveAgent, activeAgent)
	r.NotNil(passiveAgentConn)
	r.NotNil(activeAgenConn)
}
