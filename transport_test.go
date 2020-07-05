// +build !js

package ice

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/transport/test"
)

func TestStressDuplex(t *testing.T) {
	// Check for leaking routines
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 20)
	defer lim.Stop()

	// Run the test
	stressDuplex(t)
}

func testTimeout(t *testing.T, c *Conn, timeout time.Duration) {
	const pollrate = 100 * time.Millisecond
	const margin = 20 * time.Millisecond // allow 20msec error in time
	ticker := time.NewTicker(pollrate)
	defer func() {
		ticker.Stop()
		err := c.Close()
		if err != nil {
			t.Error(err)
		}
	}()

	startedAt := time.Now()

	for cnt := time.Duration(0); cnt <= timeout+defaultKeepaliveInterval+pollrate; cnt += pollrate {
		<-ticker.C

		var cs ConnectionState

		err := c.agent.run(context.Background(), func(ctx context.Context, agent *Agent) {
			cs = agent.connectionState
		})
		if err != nil {
			// we should never get here.
			panic(err)
		}

		if cs != ConnectionStateConnected {
			elapsed := time.Since(startedAt)
			if elapsed+margin < timeout {
				t.Fatalf("Connection timed out %f msec early", elapsed.Seconds()*1000)
			} else {
				t.Logf("Connection timed out in %f msec", elapsed.Seconds()*1000)
				return
			}
		}
	}
	t.Fatalf("Connection failed to time out in time. (expected timeout: %v)", timeout)
}

func TestTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	// Check for leaking routines
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 20)
	defer lim.Stop()

	t.Run("WithoutDisconnectTimeout", func(t *testing.T) {
		ca, cb := pipe(nil)
		err := cb.Close()

		if err != nil {
			// we should never get here.
			panic(err)
		}

		testTimeout(t, ca, defaultDisconnectedTimeout)
	})

	t.Run("WithDisconnectTimeout", func(t *testing.T) {
		ca, cb := pipeWithTimeout(5*time.Second, 3*time.Second)
		err := cb.Close()

		if err != nil {
			// we should never get here.
			panic(err)
		}

		testTimeout(t, ca, 5*time.Second)
	})
}

func TestReadClosed(t *testing.T) {
	// Check for leaking routines
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 20)
	defer lim.Stop()

	ca, cb := pipe(nil)

	err := ca.Close()
	if err != nil {
		// we should never get here.
		panic(err)
	}

	err = cb.Close()
	if err != nil {
		// we should never get here.
		panic(err)
	}

	empty := make([]byte, 10)
	_, err = ca.Read(empty)
	if err == nil {
		t.Fatalf("Reading from a closed channel should return an error")
	}
}

func stressDuplex(t *testing.T) {
	ca, cb := pipe(nil)

	defer func() {
		err := ca.Close()
		if err != nil {
			t.Fatal(err)
		}
		err = cb.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	opt := test.Options{
		MsgSize:  10,
		MsgCount: 1, // Order not reliable due to UDP & potentially multiple candidate pairs.
	}

	err := test.StressDuplex(ca, cb, opt)
	if err != nil {
		t.Fatal(err)
	}
}

func Benchmark(b *testing.B) {
	ca, cb := pipe(nil)
	defer func() {
		err := ca.Close()
		check(err)
		err = cb.Close()
		check(err)
	}()

	b.ResetTimer()

	opt := test.Options{
		MsgSize:  128,
		MsgCount: b.N,
	}

	err := test.StressDuplex(ca, cb, opt)
	check(err)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func gatherAndExchangeCandidates(aAgent, bAgent *Agent) {
	var wg sync.WaitGroup
	wg.Add(2)

	check(aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	check(aAgent.GatherCandidates())

	check(bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	check(bAgent.GatherCandidates())

	wg.Wait()

	candidates, err := aAgent.GetLocalCandidates()
	check(err)
	for _, c := range candidates {
		check(bAgent.AddRemoteCandidate(copyCandidate(c)))
	}

	candidates, err = bAgent.GetLocalCandidates()
	check(err)
	for _, c := range candidates {
		check(aAgent.AddRemoteCandidate(copyCandidate(c)))
	}
}

func connect(aAgent, bAgent *Agent) (*Conn, *Conn) {
	gatherAndExchangeCandidates(aAgent, bAgent)

	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		bUfrag, bPwd, acceptErr := bAgent.GetLocalUserCredentials()
		check(acceptErr)
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		check(acceptErr)
		close(accepted)
	}()
	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	check(err)
	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	check(err)

	// Ensure accepted
	<-accepted
	return aConn, bConn
}

func pipe(defaultConfig *AgentConfig) (*Conn, *Conn) {
	var urls []*URL

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg := &AgentConfig{}
	if defaultConfig != nil {
		*cfg = *defaultConfig
	}

	cfg.Urls = urls
	cfg.NetworkTypes = supportedNetworkTypes

	aAgent, err := NewAgent(cfg)
	check(err)
	check(aAgent.OnConnectionStateChange(aNotifier))

	bAgent, err := NewAgent(cfg)
	check(err)

	check(bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connect(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func pipeWithTimeout(disconnectTimeout time.Duration, iceKeepalive time.Duration) (*Conn, *Conn) {
	var urls []*URL

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg := &AgentConfig{
		Urls:                urls,
		DisconnectedTimeout: &disconnectTimeout,
		KeepaliveInterval:   &iceKeepalive,
		NetworkTypes:        supportedNetworkTypes,
	}

	aAgent, err := NewAgent(cfg)
	check(err)
	check(aAgent.OnConnectionStateChange(aNotifier))

	bAgent, err := NewAgent(cfg)
	check(err)
	check(bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connect(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func copyCandidate(o Candidate) (c Candidate) {
	candidateID := o.ID()
	var err error
	switch orig := o.(type) {
	case *CandidateHost:
		config := CandidateHostConfig{
			CandidateID: candidateID,
			Network:     udp,
			Address:     orig.address,
			Port:        orig.port,
			Component:   orig.component,
		}
		c, err = NewCandidateHost(&config)
	case *CandidateServerReflexive:
		config := CandidateServerReflexiveConfig{
			CandidateID: candidateID,
			Network:     udp,
			Address:     orig.address,
			Port:        orig.port,
			Component:   orig.component,
			RelAddr:     orig.relatedAddress.Address,
			RelPort:     orig.relatedAddress.Port,
		}
		c, err = NewCandidateServerReflexive(&config)
	case *CandidateRelay:
		config := CandidateRelayConfig{
			CandidateID: candidateID,
			Network:     udp,
			Address:     orig.address,
			Port:        orig.port,
			Component:   orig.component,
			RelAddr:     orig.relatedAddress.Address,
			RelPort:     orig.relatedAddress.Port,
		}
		c, err = NewCandidateRelay(&config)
	default:
		panic("Tried to copy unsupported candidate type")
	}

	if err != nil {
		panic(err)
	}
	return c
}

func onConnected() (func(ConnectionState), chan struct{}) {
	done := make(chan struct{})
	return func(state ConnectionState) {
		if state == ConnectionStateConnected {
			close(done)
		}
	}, done
}

func randomPort(t testing.TB) int {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to pickPort: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()
	switch addr := conn.LocalAddr().(type) {
	case *net.UDPAddr:
		return addr.Port
	default:
		t.Fatalf("unknown addr type %T", addr)
		return 0
	}
}

func TestConnStats(t *testing.T) {
	// Check for leaking routines
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 20)
	defer lim.Stop()

	ca, cb := pipe(nil)
	if _, err := ca.Write(make([]byte, 10)); err != nil {
		t.Fatal("unexpected error trying to write")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		buf := make([]byte, 10)
		if _, err := cb.Read(buf); err != nil {
			panic(errors.New("unexpected error trying to read"))
		}
		wg.Done()
	}()

	wg.Wait()

	if ca.BytesSent() != 10 {
		t.Fatal("bytes sent don't match")
	}

	if cb.BytesReceived() != 10 {
		t.Fatal("bytes received don't match")
	}

	err := ca.Close()
	if err != nil {
		// we should never get here.
		panic(err)
	}

	err = cb.Close()
	if err != nil {
		// we should never get here.
		panic(err)
	}
}
