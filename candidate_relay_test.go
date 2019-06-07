package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/transport/test"
	"github.com/pion/turn"
)

type mockTURNServer struct {
}

func (m *mockTURNServer) AuthenticateRequest(username string, srcAddr net.Addr) (password string, ok bool) {
	return "password", true
}

func TestRelayOnlyConnection(t *testing.T) {
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	serverPort := randomPort(t)
	server := turn.Create(turn.StartArguments{
		Server: &mockTURNServer{},
		Realm:  "localhost",
	})

	serverChan := make(chan error, 1)
	go func() {
		serverChan <- server.Listen("0.0.0.0", serverPort)
	}()

	cfg := &AgentConfig{
		NetworkTypes: supportedNetworkTypes,
		Urls: []*URL{
			{
				Scheme:   SchemeTypeTURN,
				Host:     "localhost",
				Username: "username",
				Password: "password",
				Port:     serverPort,
			},
		},
		CandidateTypes: []CandidateType{CandidateTypeRelay},
	}

	aAgent, err := NewAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}

	aNotifier, aConnected := onConnected()
	if err = aAgent.OnConnectionStateChange(aNotifier); err != nil {
		t.Fatal(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}

	bNotifier, bConnected := onConnected()
	if err = bAgent.OnConnectionStateChange(bNotifier); err != nil {
		t.Fatal(err)
	}

	connect(aAgent, bAgent)
	<-aConnected
	<-bConnected

	if err = aAgent.Close(); err != nil {
		t.Fatal(err)
	}
	if err = bAgent.Close(); err != nil {
		t.Fatal(err)
	}
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	<-serverChan
}
