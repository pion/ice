// Package ice implements the Interactive Connectivity Establishment (ICE)
// protocol defined in rfc5245.
package ice

import (
	"bytes"
	"math/rand"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/packetio"
)

const (
	// taskLoopInterval is the interval at which the agent performs checks
	defaultTaskLoopInterval = 2 * time.Second

	// keepaliveInterval used to keep candidates alive
	defaultKeepaliveInterval = 10 * time.Second

	// defaultConnectionTimeout used to declare a connection dead
	defaultConnectionTimeout = 30 * time.Second

	// the number of bytes that can be buffered before we start to error
	maxBufferSize = 1000 * 1000 // 1MB

	// the number of outbound binding requests we cache
	maxPendingBindingRequests = 50

	stunAttrHeaderLength = 4
)

type candidatePairs []*candidatePair

func (cp candidatePairs) Len() int      { return len(cp) }
func (cp candidatePairs) Swap(i, j int) { cp[i], cp[j] = cp[j], cp[i] }

type byPairPriority struct{ candidatePairs }

// NB: Reverse sort so our candidates start at highest priority
func (bp byPairPriority) Less(i, j int) bool {
	return bp.candidatePairs[i].Priority() > bp.candidatePairs[j].Priority()
}

type bindingRequest struct {
	transactionID []byte
	destination   net.Addr
}

// Agent represents the ICE agent
type Agent struct {
	onConnectionStateChangeHdlr       func(ConnectionState)
	onSelectedCandidatePairChangeHdlr func(*Candidate, *Candidate)

	// Used to block double Dial/Accept
	opened bool

	// State owned by the taskLoop
	taskChan        chan task
	onConnected     chan struct{}
	onConnectedOnce sync.Once

	connectivityChan <-chan time.Time
	// force candidate to be contacted immediately (instead of waiting for connectivityChan)
	forceCandidateContact chan bool

	tieBreaker      uint64
	connectionState ConnectionState
	gatheringState  GatheringState

	haveStarted   atomic.Value
	isControlling bool

	portmin uint16
	portmax uint16

	//How long should a pair stay quiet before we declare it dead?
	//0 means never timeout
	connectionTimeout time.Duration

	//How often should we send keepalive packets?
	//0 means never
	keepaliveInterval time.Duration

	// How after should we run our internal taskLoop
	taskLoopInterval time.Duration

	localUfrag      string
	localPwd        string
	localCandidates map[NetworkType][]*Candidate

	remoteUfrag      string
	remotePwd        string
	remoteCandidates map[NetworkType][]*Candidate

	selectedPair *candidatePair
	validPairs   []*candidatePair

	buffer *packetio.Buffer

	// LRU of outbound Binding request Transaction IDs
	pendingBindingRequests []bindingRequest

	// State for closing
	done chan struct{}
	err  atomicError

	log logging.LeveledLogger
}

func (a *Agent) ok() error {
	select {
	case <-a.done:
		return a.getErr()
	default:
	}
	return nil
}

func (a *Agent) getErr() error {
	err := a.err.Load()
	if err != nil {
		return err
	}
	return ErrClosed
}

// AgentConfig collects the arguments to ice.Agent construction into
// a single structure, for future-proofness of the interface
type AgentConfig struct {
	Urls []*URL

	// PortMin and PortMax are optional. Leave them 0 for the default UDP port allocation strategy.
	PortMin uint16
	PortMax uint16

	// ConnectionTimeout defaults to 30 seconds when this property is nil.
	// If the duration is 0, we will never timeout this connection.
	ConnectionTimeout *time.Duration
	// KeepaliveInterval determines how often should we send ICE
	// keepalives (should be less then connectiontimeout above)
	// when this is nil, it defaults to 10 seconds.
	// A keepalive interval of 0 means we never send keepalive packets
	KeepaliveInterval *time.Duration

	// NetworkTypes is an optional configuration for disabling or enablding
	// support for specific network types.
	NetworkTypes []NetworkType

	LoggerFactory logging.LoggerFactory

	// taskLoopInterval controls how often our internal task loop runs, this
	// task loop handles things like sending keepAlives. This is only value for testing
	// keepAlive behavior should be modified with KeepaliveInterval and ConnectionTimeout
	taskLoopInterval time.Duration
}

// NewAgent creates a new Agent
func NewAgent(config *AgentConfig) (*Agent, error) {
	if config.PortMax < config.PortMin {
		return nil, ErrPort
	}

	loggerFactory := config.LoggerFactory
	if loggerFactory == nil {
		loggerFactory = logging.NewDefaultLoggerFactory()
	}

	a := &Agent{
		tieBreaker:             rand.New(rand.NewSource(time.Now().UnixNano())).Uint64(),
		gatheringState:         GatheringStateComplete, // TODO trickle-ice
		connectionState:        ConnectionStateNew,
		localCandidates:        make(map[NetworkType][]*Candidate),
		remoteCandidates:       make(map[NetworkType][]*Candidate),
		pendingBindingRequests: make([]bindingRequest, 0, maxPendingBindingRequests),

		localUfrag:  randSeq(16),
		localPwd:    randSeq(32),
		taskChan:    make(chan task),
		onConnected: make(chan struct{}),
		buffer:      packetio.NewBuffer(),
		done:        make(chan struct{}),
		portmin:     config.PortMin,
		portmax:     config.PortMax,
		log:         loggerFactory.NewLogger("ice"),

		forceCandidateContact: make(chan bool, 1),
	}
	a.haveStarted.Store(false)

	// Make sure the buffer doesn't grow indefinitely.
	// NOTE: We actually won't get anywhere close to this limit.
	// SRTP will constantly read from the endpoint and drop packets if it's full.
	a.buffer.SetLimitSize(maxBufferSize)

	// connectionTimeout used to declare a connection dead
	if config.ConnectionTimeout == nil {
		a.connectionTimeout = defaultConnectionTimeout
	} else {
		a.connectionTimeout = *config.ConnectionTimeout
	}

	if config.KeepaliveInterval == nil {
		a.keepaliveInterval = defaultKeepaliveInterval
	} else {
		a.keepaliveInterval = *config.KeepaliveInterval
	}

	if config.taskLoopInterval == 0 {
		a.taskLoopInterval = defaultTaskLoopInterval
	} else {
		a.taskLoopInterval = config.taskLoopInterval
	}

	// Initialize local candidates
	gatherCandidatesLocal(a, config.NetworkTypes)
	gatherCandidatesReflective(a, config.Urls, config.NetworkTypes)

	go a.taskLoop()
	return a, nil
}

// OnConnectionStateChange sets a handler that is fired when the connection state changes
func (a *Agent) OnConnectionStateChange(f func(ConnectionState)) error {
	return a.run(func(agent *Agent) {
		agent.onConnectionStateChangeHdlr = f
	})
}

// OnSelectedCandidatePairChange sets a handler that is fired when the final candidate
// pair is selected
func (a *Agent) OnSelectedCandidatePairChange(f func(*Candidate, *Candidate)) error {
	return a.run(func(agent *Agent) {
		agent.onSelectedCandidatePairChangeHdlr = f
	})
}

func (a *Agent) onSelectedCandidatePairChange(p *candidatePair) {
	if p != nil {
		if a.onSelectedCandidatePairChangeHdlr != nil {
			a.onSelectedCandidatePairChangeHdlr(p.local, p.remote)
		}
	}
}

func (a *Agent) startConnectivityChecks(isControlling bool, remoteUfrag, remotePwd string) error {
	switch {
	case a.haveStarted.Load():
		return ErrMultipleStart
	case remoteUfrag == "":
		return ErrRemoteUfragEmpty
	case remotePwd == "":
		return ErrRemotePwdEmpty
	}
	a.haveStarted.Store(true)
	a.log.Debugf("Started agent: isControlling? %t, remoteUfrag: %q, remotePwd: %q", isControlling, remoteUfrag, remotePwd)

	return a.run(func(agent *Agent) {
		agent.isControlling = isControlling
		agent.remoteUfrag = remoteUfrag
		agent.remotePwd = remotePwd

		agent.updateConnectionState(ConnectionStateChecking)

		// TODO this should be dynamic, and grow when the connection is stable
		agent.forceCandidateContact <- true
		t := time.NewTicker(a.taskLoopInterval)
		agent.connectivityChan = t.C
	})
}

func (a *Agent) pingCandidate(local, remote *Candidate) {
	var msg *stun.Message
	var err error

	// The controlling agent MUST include the USE-CANDIDATE attribute in
	// order to nominate a candidate pair (Section 8.1.1).  The controlled
	// agent MUST NOT include the USE-CANDIDATE attribute in a Binding
	// request.

	transactionID := stun.GenerateTransactionID()
	if a.isControlling {
		msg, err = stun.Build(stun.ClassRequest, stun.MethodBinding, transactionID,
			&stun.Username{Username: a.remoteUfrag + ":" + a.localUfrag},
			&stun.UseCandidate{},
			&stun.IceControlling{TieBreaker: a.tieBreaker},
			&stun.Priority{Priority: local.Priority()},
			&stun.MessageIntegrity{
				Key: []byte(a.remotePwd),
			},
			&stun.Fingerprint{},
		)
	} else {
		msg, err = stun.Build(stun.ClassRequest, stun.MethodBinding, transactionID,
			&stun.Username{Username: a.remoteUfrag + ":" + a.localUfrag},
			&stun.IceControlled{TieBreaker: a.tieBreaker},
			&stun.Priority{Priority: local.Priority()},
			&stun.MessageIntegrity{
				Key: []byte(a.remotePwd),
			},
			&stun.Fingerprint{},
		)
	}

	if err != nil {
		a.log.Debug(err.Error())
		return
	}

	a.log.Tracef("ping STUN from %s to %s\n", local.String(), remote.String())
	if overflow := len(a.pendingBindingRequests) - maxPendingBindingRequests; overflow > 1 {
		a.log.Debugf("Discarded %d pending binding requests, pendingBindingRequests is full", overflow)
		a.pendingBindingRequests = a.pendingBindingRequests[overflow:]
	}

	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		transactionID: transactionID,
		destination:   remote.addr(),
	})
	a.sendSTUN(msg, local, remote)
}

func (a *Agent) updateConnectionState(newState ConnectionState) {
	if a.connectionState != newState {
		a.log.Infof("Setting new connection state: %s", newState)
		a.connectionState = newState
		hdlr := a.onConnectionStateChangeHdlr
		if hdlr != nil {
			// Call handler async since we may be holding the agent lock
			// and the handler may also require it
			go hdlr(newState)
		}
	}
}

func (a *Agent) setValidPair(local, remote *Candidate, selected, controlling bool) {
	// TODO: avoid duplicates
	p := newCandidatePair(local, remote, controlling)
	a.log.Tracef("Found valid candidate pair: %s (selected? %t)", p, selected)

	if selected {
		// Notify when the selected pair changes
		if !a.selectedPair.Equal(p) {
			a.onSelectedCandidatePairChange(p)
		}
		a.selectedPair = p
		a.validPairs = nil
		// TODO: only set state to connected on selecting final pair?
		a.updateConnectionState(ConnectionStateConnected)
	} else {
		// keep track of pairs with succesfull bindings since any of them
		// can be used for communication until the final pair is selected:
		// https://tools.ietf.org/html/draft-ietf-ice-rfc5245bis-20#section-12
		a.validPairs = append(a.validPairs, p)
		// Sort the candidate pairs by priority of the remotes
		sort.Sort(byPairPriority{a.validPairs})
	}

	// Signal connected
	a.onConnectedOnce.Do(func() { close(a.onConnected) })
}

// A task is a
type task func(*Agent)

func (a *Agent) run(t task) error {
	err := a.ok()
	if err != nil {
		return err
	}

	select {
	case <-a.done:
		return a.getErr()
	case a.taskChan <- t:
	}
	return nil
}

func (a *Agent) taskLoop() {
	contactCandidates := func() {
		if a.validateSelectedPair() {
			a.log.Trace("checking keepalive")
			a.checkKeepalive()
		} else {
			a.log.Trace("pinging all candidates")
			a.pingAllCandidates()
		}
	}

	for {
		select {
		case <-a.forceCandidateContact:
			contactCandidates()
		case <-a.connectivityChan:
			contactCandidates()
		case t := <-a.taskChan:
			// Run the task
			t(a)

		case <-a.done:
			return
		}
	}
}

// validateSelectedPair checks if the selected pair is (still) valid
// Note: the caller should hold the agent lock.
func (a *Agent) validateSelectedPair() bool {
	if a.selectedPair == nil {
		// Not valid since not selected
		return false
	}

	if (a.connectionTimeout != 0) &&
		(time.Since(a.selectedPair.remote.LastReceived()) > a.connectionTimeout) {
		a.selectedPair = nil
		a.updateConnectionState(ConnectionStateDisconnected)
		return false
	}

	return true
}

// checkKeepalive sends STUN Binding Indications to the selected pair
// if no packet has been sent on that pair in the last keepaliveInterval
// Note: the caller should hold the agent lock.
func (a *Agent) checkKeepalive() {
	if a.selectedPair == nil {
		return
	}

	if (a.keepaliveInterval != 0) &&
		(time.Since(a.selectedPair.local.LastSent()) > a.keepaliveInterval) {
		a.keepaliveCandidate(a.selectedPair.local, a.selectedPair.remote)
	}
}

// pingAllCandidates sends STUN Binding Requests to all candidates
// Note: the caller should hold the agent lock.
func (a *Agent) pingAllCandidates() {
	for networkType, localCandidates := range a.localCandidates {
		if remoteCandidates, ok := a.remoteCandidates[networkType]; ok {

			for _, localCandidate := range localCandidates {
				for _, remoteCandidate := range remoteCandidates {
					a.pingCandidate(localCandidate, remoteCandidate)
				}
			}

		}
	}
}

// AddRemoteCandidate adds a new remote candidate
func (a *Agent) AddRemoteCandidate(c *Candidate) error {
	return a.run(func(agent *Agent) {
		agent.addRemoteCandidate(c)
	})
}

// addRemoteCandidate assumes you are holding the lock (must be execute using a.run)
func (a *Agent) addRemoteCandidate(c *Candidate) {
	networkType := c.NetworkType
	set := a.remoteCandidates[networkType]

	for _, candidate := range set {
		if candidate.Equal(c) {
			return
		}
	}

	set = append(set, c)
	a.remoteCandidates[networkType] = set
}

// GetLocalCandidates returns the local candidates
func (a *Agent) GetLocalCandidates() ([]*Candidate, error) {
	res := make(chan []*Candidate)

	err := a.run(func(agent *Agent) {
		var candidates []*Candidate
		for _, set := range agent.localCandidates {
			candidates = append(candidates, set...)
		}
		res <- candidates
	})
	if err != nil {
		return nil, err
	}

	return <-res, nil
}

// GetLocalUserCredentials returns the local user credentials
func (a *Agent) GetLocalUserCredentials() (frag string, pwd string) {
	return a.localUfrag, a.localPwd
}

// Close cleans up the Agent
func (a *Agent) Close() error {
	done := make(chan struct{})
	err := a.run(func(agent *Agent) {
		defer func() {
			close(done)
		}()
		agent.err.Store(ErrClosed)
		close(agent.done)

		// Cleanup all candidates
		for net, cs := range agent.localCandidates {
			for _, c := range cs {
				err := c.close()
				if err != nil {
					a.log.Warnf("Failed to close candidate %s: %v", c, err)
				}
			}
			delete(agent.localCandidates, net)
		}
		for net, cs := range agent.remoteCandidates {
			for _, c := range cs {
				err := c.close()
				if err != nil {
					a.log.Warnf("Failed to close candidate %s: %v", c, err)
				}
			}
			delete(agent.remoteCandidates, net)
		}

		err := a.buffer.Close()
		if err != nil {
			a.log.Warnf("failed to close buffer: %v", err)
		}
	})
	if err != nil {
		return err
	}

	<-done

	return nil
}

func (a *Agent) findRemoteCandidate(networkType NetworkType, addr net.Addr) *Candidate {
	var ip net.IP
	var port int

	switch casted := addr.(type) {
	case *net.UDPAddr:
		ip = casted.IP
		port = casted.Port
	case *net.TCPAddr:
		ip = casted.IP
		port = casted.Port
	default:
		a.log.Warnf("unsupported address type %T", a)
		return nil
	}

	set := a.remoteCandidates[networkType]
	for _, c := range set {
		base := c
		if base.IP.Equal(ip) &&
			base.Port == port {
			return c
		}
	}
	return nil
}

func (a *Agent) sendBindingSuccess(m *stun.Message, local, remote *Candidate) {
	base := remote
	if out, err := stun.Build(stun.ClassSuccessResponse, stun.MethodBinding, m.TransactionID,
		&stun.XorMappedAddress{
			XorAddress: stun.XorAddress{
				IP:   base.IP,
				Port: base.Port,
			},
		},
		&stun.MessageIntegrity{
			Key: []byte(a.localPwd),
		},
		&stun.Fingerprint{},
	); err != nil {
		a.log.Warnf("Failed to handle inbound ICE from: %s to: %s error: %s", local, remote, err)
	} else {
		a.sendSTUN(out, local, remote)
	}
}

func (a *Agent) handleInboundControlled(m *stun.Message, localCandidate, remoteCandidate *Candidate) {
	if _, isControlled := m.GetOneAttribute(stun.AttrIceControlled); isControlled && !a.isControlling {
		a.log.Debug("inbound isControlled && a.isControlling == false")
		return
	}

	successResponse := m.Method == stun.MethodBinding && m.Class == stun.ClassSuccessResponse
	a.log.Tracef("got controlled message (success? %t)", successResponse)
	if successResponse {
		// Remember the working pair and select it when marked with usepair
		a.setValidPair(localCandidate, remoteCandidate, true, false)
	} else {
		// Send success response
		a.sendBindingSuccess(m, localCandidate, remoteCandidate)
	}
}

func (a *Agent) handleInboundControlling(m *stun.Message, localCandidate, remoteCandidate *Candidate) {
	if _, isControlling := m.GetOneAttribute(stun.AttrIceControlling); isControlling && a.isControlling {
		a.log.Debug("inbound isControlling && a.isControlling == true")
		return
	} else if _, useCandidate := m.GetOneAttribute(stun.AttrUseCandidate); useCandidate && a.isControlling {
		a.log.Debug("useCandidate && a.isControlling == true")
		return
	}

	successResponse := m.Method == stun.MethodBinding && m.Class == stun.ClassSuccessResponse
	a.log.Tracef("got controlled message (success? %t)", successResponse)
	if successResponse {
		// Remember the working pair and select it when receiving a success response
		a.setValidPair(localCandidate, remoteCandidate, true, true)
	} else {
		// Send success response
		a.sendBindingSuccess(m, localCandidate, remoteCandidate)
	}
}

// Assert that the passed TransactionID is in our pendingBindingRequests and returns the destination
// If the bindingRequest was valid remove it from our pending cache
func (a *Agent) handleInboundBindingSuccess(id []byte) (bool, net.Addr) {
	for i := range a.pendingBindingRequests {
		if bytes.Equal(a.pendingBindingRequests[i].transactionID, id) {
			defer func() {
				a.pendingBindingRequests = append(a.pendingBindingRequests[:i], a.pendingBindingRequests[i+1:]...)
			}()
			return true, a.pendingBindingRequests[i].destination
		}
	}
	return false, nil
}

// handleInbound processes STUN traffic from a remote candidate
func (a *Agent) handleInbound(m *stun.Message, local *Candidate, remote net.Addr) {
	var err error
	if m == nil || local == nil {
		return
	} else if m.Method != stun.MethodBinding || !(m.Class == stun.ClassSuccessResponse || m.Class == stun.ClassRequest) {
		a.log.Tracef("unhandled STUN from %s to %s class(%s) method(%s)", remote.String(), local.String(), m.Method.String(), m.Class.String())
		return
	}

	remoteCandidate := a.findRemoteCandidate(local.NetworkType, remote)
	if m.Class == stun.ClassSuccessResponse {
		if err = assertInboundMessageIntegrity(m, []byte(a.remotePwd)); err != nil {
			a.log.Warnf("discard message from (%s), %v", remote, err)
			return
		}

		ok, transactionAddr := a.handleInboundBindingSuccess(m.TransactionID)
		if !ok {
			a.log.Errorf("discard message from (%s), invalid TransactionID %s", remote, m.TransactionID)
			return
		}

		// Assert that NAT is not symmetric
		// https://tools.ietf.org/html/rfc8445#section-7.2.5.2.1
		if !addrEqual(transactionAddr, remote) {
			a.log.Debugf("discard message: transaction source and destination does not match expected(%s), actual(%s)", transactionAddr, remote)
			return
		} else if remoteCandidate == nil { // Should fail previous check, better to be safe though
			a.log.Warnf("discard success message from (%s), no such remote", remote)
			return
		}
	} else {
		if err = assertInboundUsername(m, a.localUfrag+":"+a.remoteUfrag); err != nil {
			a.log.Warnf("discard message from (%s), %v", remote, err)
			return
		} else if err = assertInboundMessageIntegrity(m, []byte(a.localPwd)); err != nil {
			a.log.Warnf("discard message from (%s), %v", remote, err)
			return
		}

		if remoteCandidate == nil {
			ip, port, networkType, ok := parseAddr(remote)
			if !ok {
				a.log.Errorf("Failed to create parse remote net.Addr when creating remote prflx candidate")
				return
			}

			prflxCandidate, err := NewCandidatePeerReflexive(networkType.String(), ip, port, local.Component, "", 0)
			if err != nil {
				a.log.Errorf("Failed to create new remote prflx candidate (%s)", err)
				return
			}
			remoteCandidate = prflxCandidate

			a.log.Debugf("adding a new peer-reflexive candiate: %s ", remote)
			a.addRemoteCandidate(remoteCandidate)
		}
	}

	a.log.Tracef("inbound STUN from %s to %s", remote.String(), local.String())
	remoteCandidate.seen(false)
	if a.isControlling {
		a.handleInboundControlling(m, local, remoteCandidate)
	} else {
		a.handleInboundControlled(m, local, remoteCandidate)
	}
}

// noSTUNSeen processes non STUN traffic from a remote candidate,
// and returns true if it is an actual remote candidate
func (a *Agent) noSTUNSeen(local *Candidate, remote net.Addr) bool {
	remoteCandidate := a.findRemoteCandidate(local.NetworkType, remote)
	if remoteCandidate == nil {
		return false
	}

	remoteCandidate.seen(false)
	return true
}

func (a *Agent) getBestPair() (*candidatePair, error) {
	res := make(chan *candidatePair)

	err := a.run(func(agent *Agent) {
		if agent.selectedPair != nil {
			res <- agent.selectedPair
			return
		}
		for _, p := range agent.validPairs {
			res <- p
			return
		}
		res <- nil
	})

	if err != nil {
		return nil, err
	}

	out := <-res

	if out == nil {
		return nil, ErrNoCandidatePairs
	}

	return out, nil
}
