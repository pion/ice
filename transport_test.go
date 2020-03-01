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
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 20)
	defer lim.Stop()

	// Check for leaking routines
	report := test.CheckRoutines(t)
	defer report()

	// Run the test
	stressDuplex(t)
}

func testTimeout(t *testing.T, c *Conn, timeout time.Duration) {
	const pollrate = 100 * time.Millisecond
	const margin = 20 * time.Millisecond // allow 20msec error in time
	statechan := make(chan ConnectionState, 1)
	ticker := time.NewTicker(pollrate)

	startedAt := time.Now()

	for cnt := time.Duration(0); cnt <= timeout+defaultTaskLoopInterval; cnt += pollrate {
		<-ticker.C

		err := c.agent.run(func(agent *Agent) {
			statechan <- agent.connectionState
		})
		if err != nil {
			// we should never get here.
			panic(err)
		}

		cs := <-statechan
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
	t.Fatalf("Connection failed to time out in time.")
}

func TestTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	ca, cb := pipe()
	err := cb.Close()

	if err != nil {
		// we should never get here.
		panic(err)
	}

	testTimeout(t, ca, defaultConnectionTimeout)

	ca, cb = pipeWithTimeout(5*time.Second, 3*time.Second)
	err = cb.Close()

	if err != nil {
		// we should never get here.
		panic(err)
	}

	testTimeout(t, ca, 5*time.Second)
}

func TestReadClosed(t *testing.T) {
	ca, cb := pipe()

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
	ca, cb := pipe()

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
	ca, cb := pipe()
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

func connect(aAgent, bAgent *Agent) (*Conn, *Conn) {
	// Manual signaling
	aUfrag, aPwd := aAgent.GetLocalUserCredentials()
	bUfrag, bPwd := bAgent.GetLocalUserCredentials()

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

	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		check(acceptErr)
		close(accepted)
	}()

	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	check(err)

	// Ensure accepted
	<-accepted
	return aConn, bConn
}

func pipe() (*Conn, *Conn) {
	var urls []*URL

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	var wg sync.WaitGroup
	wg.Add(2)

	cfg := &AgentConfig{
		Urls:         urls,
		Trickle:      true,
		NetworkTypes: supportedNetworkTypes,
	}

	aAgent, err := NewAgent(cfg)
	if err != nil {
		panic(err)
	}
	err = aAgent.OnConnectionStateChange(aNotifier)
	if err != nil {
		panic(err)
	}
	err = aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	})
	if err != nil {
		panic(err)
	}
	err = aAgent.GatherCandidates()
	if err != nil {
		panic(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		panic(err)
	}
	err = bAgent.OnConnectionStateChange(bNotifier)
	if err != nil {
		panic(err)
	}
	err = bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	})
	if err != nil {
		panic(err)
	}
	err = bAgent.GatherCandidates()
	if err != nil {
		panic(err)
	}

	wg.Wait()
	aConn, bConn := connect(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func pipeWithTimeout(iceTimeout time.Duration, iceKeepalive time.Duration) (*Conn, *Conn) {
	var urls []*URL

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	var wg sync.WaitGroup
	wg.Add(2)

	cfg := &AgentConfig{
		Urls:              urls,
		Trickle:           true,
		ConnectionTimeout: &iceTimeout,
		KeepaliveInterval: &iceKeepalive,
		NetworkTypes:      supportedNetworkTypes,
	}

	aAgent, err := NewAgent(cfg)
	if err != nil {
		panic(err)
	}
	err = aAgent.OnConnectionStateChange(aNotifier)
	if err != nil {
		panic(err)
	}
	err = aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	})
	if err != nil {
		panic(err)
	}
	err = aAgent.GatherCandidates()
	if err != nil {
		panic(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		panic(err)
	}
	err = bAgent.OnConnectionStateChange(bNotifier)
	if err != nil {
		panic(err)
	}
	err = bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	})
	if err != nil {
		panic(err)
	}
	err = bAgent.GatherCandidates()
	if err != nil {
		panic(err)
	}

	wg.Wait()
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
	ca, cb := pipe()
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
