package ice

import (
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/test"
	"github.com/pion/turn"
)

func TestServerReflexiveOnlyConnection(t *testing.T) {
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	loggerFactory := logging.NewDefaultLoggerFactory()
	//log := loggerFactory.NewLogger("test")

	serverPort := randomPort(t)
	server := turn.NewServer(&turn.ServerConfig{
		Realm:         "pion.ly",
		AuthHandler:   optimisticAuthHandler,
		ListeningPort: serverPort,
		LoggerFactory: loggerFactory,
	})
	err := server.AddListeningIPAddr("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	err = server.Start()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &AgentConfig{
		NetworkTypes: []NetworkType{NetworkTypeUDP4},
		Urls: []*URL{
			{
				Scheme: SchemeTypeSTUN,
				Host:   "localhost",
				Port:   serverPort,
			},
		},
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive},
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
}
