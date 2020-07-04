// Package ice implements the Interactive Connectivity Establishment (ICE)
// protocol defined in rfc5245.
package ice

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/mdns"
	"github.com/pion/stun"
	"github.com/pion/transport/packetio"
	"github.com/pion/transport/vnet"
)

type bindingRequest struct {
	timestamp      time.Time
	transactionID  [stun.TransactionIDSize]byte
	destination    net.Addr
	isUseCandidate bool
}

// Agent represents the ICE agent
type Agent struct {
	// Lock for transactional operations on Agent. Unlike a mutex
	// all queued lock attempts are canceled when .Close() is called
	muChan chan struct{}

	onConnectionStateChangeHdlr       atomic.Value // func(ConnectionState)
	onSelectedCandidatePairChangeHdlr atomic.Value // func(Candidate, Candidate)
	onCandidateHdlr                   atomic.Value // func(Candidate)

	// State owned by the taskLoop
	onConnected     chan struct{}
	onConnectedOnce sync.Once

	connectivityTicker *time.Ticker
	// force candidate to be contacted immediately (instead of waiting for connectivityTicker)
	forceCandidateContact chan bool

	tieBreaker uint64
	lite       bool

	connectionState ConnectionState
	gatheringState  GatheringState

	mDNSMode MulticastDNSMode
	mDNSName string
	mDNSConn *mdns.Conn

	muHaveStarted sync.Mutex
	startedCh     <-chan struct{}
	startedFn     func()
	isControlling bool

	maxBindingRequests uint16

	candidateSelectionTimeout time.Duration
	hostAcceptanceMinWait     time.Duration
	srflxAcceptanceMinWait    time.Duration
	prflxAcceptanceMinWait    time.Duration
	relayAcceptanceMinWait    time.Duration

	portmin uint16
	portmax uint16

	candidateTypes []CandidateType

	// How long connectivity checks can fail before the ICE Agent
	// goes to disconnected
	disconnectedTimeout time.Duration

	// How long connectivity checks can fail before the ICE Agent
	// goes to failed
	failedTimeout time.Duration

	// How often should we send keepalive packets?
	// 0 means never
	keepaliveInterval time.Duration

	// How after should we run our internal taskLoop
	taskLoopInterval time.Duration

	localUfrag      string
	localPwd        string
	localCandidates map[NetworkType][]Candidate

	remoteUfrag      string
	remotePwd        string
	remoteCandidates map[NetworkType][]Candidate

	checklist []*candidatePair
	selector  pairCandidateSelector

	selectedPair atomic.Value // *candidatePair

	urls         []*URL
	networkTypes []NetworkType

	buffer *packetio.Buffer

	// LRU of outbound Binding request Transaction IDs
	pendingBindingRequests []bindingRequest

	// 1:1 D-NAT IP address mapping
	extIPMapper *externalIPMapper

	// State for closing
	done chan struct{}
	err  atomicError

	chanCandidate chan Candidate
	chanState     chan ConnectionState

	loggerFactory logging.LoggerFactory
	log           logging.LeveledLogger

	net *vnet.Net

	interfaceFilter func(string) bool

	insecureSkipVerify bool
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
	if err := a.err.Load(); err != nil {
		return err
	}
	return ErrClosed
}

// Run an operation with the the lock taken
// If the agent is closed return an error
func (a *Agent) run(t func(*Agent), localDone <-chan struct{}) error {
	if err := a.ok(); err != nil {
		return err
	}

	select {
	case <-a.done:
		return a.getErr()
	case <-localDone:
		return ErrRunCanceled
	case a.muChan <- struct{}{}:
		var err error
		select {
		case <-localDone:
			err = ErrRunCanceled
		case <-a.done:
			// Ensure the agent is not closed
			err = a.getErr()
		default:
			t(a)
		}
		<-a.muChan
		return err
	}
}

// NewAgent creates a new Agent
func NewAgent(config *AgentConfig) (*Agent, error) {
	var err error
	if config.PortMax < config.PortMin {
		return nil, ErrPort
	}

	mDNSName := config.MulticastDNSHostName
	if mDNSName == "" {
		if mDNSName, err = generateMulticastDNSName(); err != nil {
			return nil, err
		}
	}

	if !strings.HasSuffix(mDNSName, ".local") || len(strings.Split(mDNSName, ".")) != 2 {
		return nil, ErrInvalidMulticastDNSHostName
	}

	mDNSMode := config.MulticastDNSMode
	if mDNSMode == 0 {
		mDNSMode = MulticastDNSModeQueryOnly
	}

	loggerFactory := config.LoggerFactory
	if loggerFactory == nil {
		loggerFactory = logging.NewDefaultLoggerFactory()
	}
	log := loggerFactory.NewLogger("ice")

	var mDNSConn *mdns.Conn
	mDNSConn, mDNSMode, err = createMulticastDNS(mDNSMode, mDNSName, log)
	// Opportunistic mDNS: If we can't open the connection, that's ok: we
	// can continue without it.
	if err != nil {
		log.Warnf("Failed to initialize mDNS %s: %v", mDNSName, err)
	}
	closeMDNSConn := func() {
		if mDNSConn != nil {
			if mdnsCloseErr := mDNSConn.Close(); mdnsCloseErr != nil {
				log.Warnf("Failed to close mDNS: %v", mdnsCloseErr)
			}
		}
	}

	startedCtx, startedFn := context.WithCancel(context.Background())

	a := &Agent{
		tieBreaker:       globalMathRandomGenerator.Uint64(),
		lite:             config.Lite,
		gatheringState:   GatheringStateNew,
		connectionState:  ConnectionStateNew,
		localCandidates:  make(map[NetworkType][]Candidate),
		remoteCandidates: make(map[NetworkType][]Candidate),
		urls:             config.Urls,
		networkTypes:     config.NetworkTypes,
		onConnected:      make(chan struct{}),
		buffer:           packetio.NewBuffer(),
		done:             make(chan struct{}),
		startedCh:        startedCtx.Done(),
		startedFn:        startedFn,
		chanState:        make(chan ConnectionState),
		chanCandidate:    make(chan Candidate),
		portmin:          config.PortMin,
		portmax:          config.PortMax,
		loggerFactory:    loggerFactory,
		log:              log,
		net:              config.Net,
		muChan:           make(chan struct{}, 1),

		mDNSMode: mDNSMode,
		mDNSName: mDNSName,
		mDNSConn: mDNSConn,

		forceCandidateContact: make(chan bool, 1),

		interfaceFilter: config.InterfaceFilter,

		insecureSkipVerify: config.InsecureSkipVerify,
	}

	if a.net == nil {
		a.net = vnet.NewNet(nil)
	} else if a.net.IsVirtual() {
		a.log.Warn("vnet is enabled")
		if a.mDNSMode != MulticastDNSModeDisabled {
			a.log.Warn("vnet does not support mDNS yet")
		}
	}

	config.initWithDefaults(a)

	// Make sure the buffer doesn't grow indefinitely.
	// NOTE: We actually won't get anywhere close to this limit.
	// SRTP will constantly read from the endpoint and drop packets if it's full.
	a.buffer.SetLimitSize(maxBufferSize)

	if a.lite && (len(a.candidateTypes) != 1 || a.candidateTypes[0] != CandidateTypeHost) {
		closeMDNSConn()
		return nil, ErrLiteUsingNonHostCandidates
	}

	if config.Urls != nil && len(config.Urls) > 0 && !containsCandidateType(CandidateTypeServerReflexive, a.candidateTypes) && !containsCandidateType(CandidateTypeRelay, a.candidateTypes) {
		closeMDNSConn()
		return nil, ErrUselessUrlsProvided
	}

	if err = config.initExtIPMapping(a); err != nil {
		closeMDNSConn()
		return nil, err
	}

	// Restart is also used to initialize the agent for the first time
	if err := a.Restart(config.LocalUfrag, config.LocalPwd); err != nil {
		closeMDNSConn()
		return nil, err
	}

	a.startOnConnectionStateChangeRoutine()
	return a, nil
}

// OnConnectionStateChange sets a handler that is fired when the connection state changes
func (a *Agent) OnConnectionStateChange(f func(ConnectionState)) error {
	a.onConnectionStateChangeHdlr.Store(f)
	return nil
}

// OnSelectedCandidatePairChange sets a handler that is fired when the final candidate
// pair is selected
func (a *Agent) OnSelectedCandidatePairChange(f func(Candidate, Candidate)) error {
	a.onSelectedCandidatePairChangeHdlr.Store(f)
	return nil
}

// OnCandidate sets a handler that is fired when new candidates gathered. When
// the gathering process complete the last candidate is nil.
func (a *Agent) OnCandidate(f func(Candidate)) error {
	a.onCandidateHdlr.Store(f)
	return nil
}

func (a *Agent) onSelectedCandidatePairChange(p *candidatePair) {
	if p != nil {
		if h, ok := a.onSelectedCandidatePairChangeHdlr.Load().(func(Candidate, Candidate)); ok {
			h(p.local, p.remote)
		}
	}
}

func (a *Agent) onCandidate(c Candidate) {
	if onCandidateHdlr, ok := a.onCandidateHdlr.Load().(func(Candidate)); ok {
		onCandidateHdlr(c)
	}
}

func (a *Agent) onConnectionStateChange(s ConnectionState) {
	if hdlr, ok := a.onConnectionStateChangeHdlr.Load().(func(ConnectionState)); ok {
		hdlr(s)
	}
}

func (a *Agent) startOnConnectionStateChangeRoutine() {
	go func() {
		for {
			select {
			case s, isOpen := <-a.chanState:
				if !isOpen {
					for c := range a.chanCandidate {
						a.onCandidate(c)
					}
					return
				}
				a.onConnectionStateChange(s)

			case c, isOpen := <-a.chanCandidate:
				if !isOpen {
					for s := range a.chanState {
						a.onConnectionStateChange(s)
					}
					return
				}
				a.onCandidate(c)
			}
		}
	}()
}

func (a *Agent) startConnectivityChecks(isControlling bool, remoteUfrag, remotePwd string) error {
	a.muHaveStarted.Lock()
	defer a.muHaveStarted.Unlock()
	select {
	case <-a.startedCh:
		return ErrMultipleStart
	default:
	}
	if err := a.SetRemoteCredentials(remoteUfrag, remotePwd); err != nil {
		return err
	}

	a.log.Debugf("Started agent: isControlling? %t, remoteUfrag: %q, remotePwd: %q", isControlling, remoteUfrag, remotePwd)

	return a.run(func(agent *Agent) {
		agent.isControlling = isControlling
		agent.remoteUfrag = remoteUfrag
		agent.remotePwd = remotePwd

		if isControlling {
			a.selector = &controllingSelector{agent: a, log: a.log}
		} else {
			a.selector = &controlledSelector{agent: a, log: a.log}
		}

		if a.lite {
			a.selector = &liteSelector{pairCandidateSelector: a.selector}
		}

		a.selector.Start()
		a.startedFn()

		agent.updateConnectionState(ConnectionStateChecking)

		// TODO this should be dynamic, and grow when the connection is stable
		a.requestConnectivityCheck()
		agent.connectivityTicker = time.NewTicker(a.taskLoopInterval)
		go a.connectivityChecks()
	}, nil)
}

func (a *Agent) connectivityChecks() {
	lastConnectionState := ConnectionState(0)
	checkingDuration := time.Time{}

	contact := func() {
		if err := a.run(func(a *Agent) {
			defer func() {
				lastConnectionState = a.connectionState
			}()

			switch a.connectionState {
			case ConnectionStateFailed:
				// The connection is currently failed so don't send any checks
				// In the future it may be restarted though
				return
			case ConnectionStateChecking:
				// We have just entered checking for the first time so update our checking timer
				if lastConnectionState != a.connectionState {
					checkingDuration = time.Now()
				}

				// We have been in checking longer then Disconnect+Failed timeout, set the connection to Failed
				if time.Since(checkingDuration) > a.disconnectedTimeout+a.failedTimeout {
					a.updateConnectionState(ConnectionStateFailed)
					return
				}
			}

			a.selector.ContactCandidates()
		}, nil); err != nil {
			a.log.Warnf("taskLoop failed: %v", err)
		}
	}

	for {
		select {
		case <-a.forceCandidateContact:
			contact()
		case <-a.connectivityTicker.C:
			contact()
		case <-a.done:
			return
		}
	}
}

func (a *Agent) updateConnectionState(newState ConnectionState) {
	if a.connectionState != newState {
		// Connection has gone to failed, release all gathered candidates
		if newState == ConnectionStateFailed {
			a.deleteAllCandidates()
		}

		a.log.Infof("Setting new connection state: %s", newState)
		a.connectionState = newState

		// Call handler in different routine since we may be holding the agent lock
		// and the handler may also require it
		a.chanState <- newState
	}
}

func (a *Agent) setSelectedPair(p *candidatePair) {
	a.log.Tracef("Set selected candidate pair: %s", p)
	// Notify when the selected pair changes
	a.onSelectedCandidatePairChange(p)

	if p == nil {
		var nilPair *candidatePair
		a.selectedPair.Store(nilPair)
		return
	}

	p.nominated = true
	a.selectedPair.Store(p)

	a.updateConnectionState(ConnectionStateConnected)

	// Signal connected
	a.onConnectedOnce.Do(func() { close(a.onConnected) })
}

func (a *Agent) pingAllCandidates() {
	a.log.Trace("pinging all candidates")

	if len(a.checklist) == 0 {
		a.log.Warn("pingAllCandidates called with no candidate pairs. Connection is not possible yet.")
	}

	for _, p := range a.checklist {
		if p.state == CandidatePairStateWaiting {
			p.state = CandidatePairStateInProgress
		} else if p.state != CandidatePairStateInProgress {
			continue
		}

		if p.bindingRequestCount > a.maxBindingRequests {
			a.log.Tracef("max requests reached for pair %s, marking it as failed\n", p)
			p.state = CandidatePairStateFailed
		} else {
			a.selector.PingCandidate(p.local, p.remote)
			p.bindingRequestCount++
		}
	}
}

func (a *Agent) getBestAvailableCandidatePair() *candidatePair {
	var best *candidatePair
	for _, p := range a.checklist {
		if p.state == CandidatePairStateFailed {
			continue
		}

		if best == nil {
			best = p
		} else if best.Priority() < p.Priority() {
			best = p
		}
	}
	return best
}

func (a *Agent) getBestValidCandidatePair() *candidatePair {
	var best *candidatePair
	for _, p := range a.checklist {
		if p.state != CandidatePairStateSucceeded {
			continue
		}

		if best == nil {
			best = p
		} else if best.Priority() < p.Priority() {
			best = p
		}
	}
	return best
}

func (a *Agent) addPair(local, remote Candidate) *candidatePair {
	p := newCandidatePair(local, remote, a.isControlling)
	a.checklist = append(a.checklist, p)
	return p
}

func (a *Agent) findPair(local, remote Candidate) *candidatePair {
	for _, p := range a.checklist {
		if p.local.Equal(local) && p.remote.Equal(remote) {
			return p
		}
	}
	return nil
}

// validateSelectedPair checks if the selected pair is (still) valid
// Note: the caller should hold the agent lock.
func (a *Agent) validateSelectedPair() bool {
	selectedPair := a.getSelectedPair()
	if selectedPair == nil {
		return false
	}

	disconnectedTime := time.Since(selectedPair.remote.LastReceived())

	// Only allow transitions to failed if a.failedTimeout is non-zero
	totalTimeToFailure := a.failedTimeout
	if totalTimeToFailure != 0 {
		totalTimeToFailure += a.disconnectedTimeout
	}

	switch {
	case totalTimeToFailure != 0 && disconnectedTime > totalTimeToFailure:
		a.updateConnectionState(ConnectionStateFailed)
	case a.disconnectedTimeout != 0 && disconnectedTime > a.disconnectedTimeout:
		a.updateConnectionState(ConnectionStateDisconnected)
	default:
		a.updateConnectionState(ConnectionStateConnected)
	}

	return true
}

// checkKeepalive sends STUN Binding Indications to the selected pair
// if no packet has been sent on that pair in the last keepaliveInterval
// Note: the caller should hold the agent lock.
func (a *Agent) checkKeepalive() {
	selectedPair := a.getSelectedPair()
	if selectedPair == nil {
		return
	}

	if (a.keepaliveInterval != 0) &&
		(time.Since(selectedPair.local.LastSent()) > a.keepaliveInterval) {
		// we use binding request instead of indication to support refresh consent schemas
		// see https://tools.ietf.org/html/rfc7675
		a.selector.PingCandidate(selectedPair.local, selectedPair.remote)
	}
}

// AddRemoteCandidate adds a new remote candidate
func (a *Agent) AddRemoteCandidate(c Candidate) error {
	// If we have a mDNS Candidate lets fully resolve it before adding it locally
	if c.Type() == CandidateTypeHost && strings.HasSuffix(c.Address(), ".local") {
		if a.mDNSMode == MulticastDNSModeDisabled {
			a.log.Warnf("remote mDNS candidate added, but mDNS is disabled: (%s)", c.Address())
			return nil
		}

		hostCandidate, ok := c.(*CandidateHost)
		if !ok {
			return ErrAddressParseFailed
		}

		go a.resolveAndAddMulticastCandidate(hostCandidate)
		return nil
	}

	go func() {
		if err := a.run(func(agent *Agent) {
			agent.addRemoteCandidate(c)
		}, nil); err != nil {
			a.log.Warnf("Failed to add remote candidate %s: %v", c.Address(), err)
			return
		}
	}()
	return nil
}

func (a *Agent) resolveAndAddMulticastCandidate(c *CandidateHost) {
	if a.mDNSConn == nil {
		return
	}
	_, src, err := a.mDNSConn.Query(context.TODO(), c.Address())
	if err != nil {
		a.log.Warnf("Failed to discover mDNS candidate %s: %v", c.Address(), err)
		return
	}

	ip, _, _, _ := parseAddr(src)
	if ip == nil {
		a.log.Warnf("Failed to discover mDNS candidate %s: failed to parse IP", c.Address())
		return
	}

	if err = c.setIP(ip); err != nil {
		a.log.Warnf("Failed to discover mDNS candidate %s: %v", c.Address(), err)
		return
	}

	if err = a.run(func(agent *Agent) {
		agent.addRemoteCandidate(c)
	}, nil); err != nil {
		a.log.Warnf("Failed to add mDNS candidate %s: %v", c.Address(), err)
		return
	}
}

func (a *Agent) requestConnectivityCheck() {
	select {
	case a.forceCandidateContact <- true:
	default:
	}
}

// addRemoteCandidate assumes you are holding the lock (must be execute using a.run)
func (a *Agent) addRemoteCandidate(c Candidate) {
	set := a.remoteCandidates[c.NetworkType()]

	for _, candidate := range set {
		if candidate.Equal(c) {
			return
		}
	}

	set = append(set, c)
	a.remoteCandidates[c.NetworkType()] = set

	if localCandidates, ok := a.localCandidates[c.NetworkType()]; ok {
		for _, localCandidate := range localCandidates {
			a.addPair(localCandidate, c)
		}
	}

	a.requestConnectivityCheck()
}

func (a *Agent) addCandidate(c Candidate, candidateConn net.PacketConn) error {
	return a.run(func(agent *Agent) {
		c.start(a, candidateConn, a.startedCh)

		set := a.localCandidates[c.NetworkType()]
		for _, candidate := range set {
			if candidate.Equal(c) {
				if err := c.close(); err != nil {
					a.log.Warnf("Failed to close duplicate candidate: %v", err)
				}
				return
			}
		}

		set = append(set, c)
		a.localCandidates[c.NetworkType()] = set

		if remoteCandidates, ok := a.remoteCandidates[c.NetworkType()]; ok {
			for _, remoteCandidate := range remoteCandidates {
				a.addPair(c, remoteCandidate)
			}
		}

		a.requestConnectivityCheck()

		a.chanCandidate <- c
	}, nil)
}

// GetLocalCandidates returns the local candidates
func (a *Agent) GetLocalCandidates() ([]Candidate, error) {
	res := make(chan []Candidate, 1)

	err := a.run(func(agent *Agent) {
		var candidates []Candidate
		for _, set := range agent.localCandidates {
			candidates = append(candidates, set...)
		}
		res <- candidates
	}, nil)
	if err != nil {
		return nil, err
	}

	return <-res, nil
}

// GetLocalUserCredentials returns the local user credentials
func (a *Agent) GetLocalUserCredentials() (frag string, pwd string, err error) {
	valSet := make(chan struct{})
	err = a.run(func(agent *Agent) {
		frag = agent.localUfrag
		pwd = agent.localPwd
		close(valSet)
	}, nil)

	if err == nil {
		<-valSet
	}
	return
}

// GetRemoteUserCredentials returns the remote user credentials
func (a *Agent) GetRemoteUserCredentials() (frag string, pwd string, err error) {
	valSet := make(chan struct{})
	err = a.run(func(agent *Agent) {
		frag = agent.remoteUfrag
		pwd = agent.remotePwd
		close(valSet)
	}, nil)

	if err == nil {
		<-valSet
	}
	return
}

// Close cleans up the Agent
func (a *Agent) Close() error {
	done := make(chan struct{})
	err := a.run(func(agent *Agent) {
		defer func() {
			close(done)
			close(agent.chanState)
			close(agent.chanCandidate)
		}()
		agent.err.Store(ErrClosed)
		close(agent.done)

		a.deleteAllCandidates()
		a.startedFn()

		if err := a.buffer.Close(); err != nil {
			a.log.Warnf("failed to close buffer: %v", err)
		}

		if a.connectivityTicker != nil {
			a.connectivityTicker.Stop()
		}

		a.closeMulticastConn()
		a.updateConnectionState(ConnectionStateClosed)
	}, nil)
	if err != nil {
		return err
	}

	<-done
	return nil
}

// Remove all candidates. This closes any listening sockets
// and removes both the local and remote candidate lists.
//
// This is used for restarts, failures and on close
func (a *Agent) deleteAllCandidates() {
	for net, cs := range a.localCandidates {
		for _, c := range cs {
			if err := c.close(); err != nil {
				a.log.Warnf("Failed to close candidate %s: %v", c, err)
			}
		}
		delete(a.localCandidates, net)
	}
	for net, cs := range a.remoteCandidates {
		for _, c := range cs {
			if err := c.close(); err != nil {
				a.log.Warnf("Failed to close candidate %s: %v", c, err)
			}
		}
		delete(a.remoteCandidates, net)
	}
}

func (a *Agent) findRemoteCandidate(networkType NetworkType, addr net.Addr) Candidate {
	ip, port, err := addrIPAndPort(addr)
	if err != nil {
		a.log.Warn(err.Error())
		return nil
	}

	set := a.remoteCandidates[networkType]
	for _, c := range set {
		if c.Address() == ip.String() && c.Port() == port {
			return c
		}
	}
	return nil
}

func (a *Agent) sendBindingRequest(m *stun.Message, local, remote Candidate) {
	a.log.Tracef("ping STUN from %s to %s\n", local.String(), remote.String())

	a.invalidatePendingBindingRequests(time.Now())
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp:      time.Now(),
		transactionID:  m.TransactionID,
		destination:    remote.addr(),
		isUseCandidate: m.Contains(stun.AttrUseCandidate),
	})

	a.sendSTUN(m, local, remote)
}

func (a *Agent) sendBindingSuccess(m *stun.Message, local, remote Candidate) {
	base := remote
	if out, err := stun.Build(m, stun.BindingSuccess,
		&stun.XORMappedAddress{
			IP:   base.addr().IP,
			Port: base.addr().Port,
		},
		stun.NewShortTermIntegrity(a.localPwd),
		stun.Fingerprint,
	); err != nil {
		a.log.Warnf("Failed to handle inbound ICE from: %s to: %s error: %s", local, remote, err)
	} else {
		a.sendSTUN(out, local, remote)
	}
}

/* Removes pending binding requests that are over maxBindingRequestTimeout old

   Let HTO be the transaction timeout, which SHOULD be 2*RTT if
   RTT is known or 500 ms otherwise.
   https://tools.ietf.org/html/rfc8445#appendix-B.1
*/
func (a *Agent) invalidatePendingBindingRequests(filterTime time.Time) {
	initialSize := len(a.pendingBindingRequests)

	temp := a.pendingBindingRequests[:0]
	for _, bindingRequest := range a.pendingBindingRequests {
		if filterTime.Sub(bindingRequest.timestamp) < maxBindingRequestTimeout {
			temp = append(temp, bindingRequest)
		}
	}

	a.pendingBindingRequests = temp
	if bindRequestsRemoved := initialSize - len(a.pendingBindingRequests); bindRequestsRemoved > 0 {
		a.log.Tracef("Discarded %d binding requests because they expired", bindRequestsRemoved)
	}
}

// Assert that the passed TransactionID is in our pendingBindingRequests and returns the destination
// If the bindingRequest was valid remove it from our pending cache
func (a *Agent) handleInboundBindingSuccess(id [stun.TransactionIDSize]byte) (bool, *bindingRequest) {
	a.invalidatePendingBindingRequests(time.Now())
	for i := range a.pendingBindingRequests {
		if a.pendingBindingRequests[i].transactionID == id {
			validBindingRequest := a.pendingBindingRequests[i]
			a.pendingBindingRequests = append(a.pendingBindingRequests[:i], a.pendingBindingRequests[i+1:]...)
			return true, &validBindingRequest
		}
	}
	return false, nil
}

// handleInbound processes STUN traffic from a remote candidate
func (a *Agent) handleInbound(m *stun.Message, local Candidate, remote net.Addr) {
	var err error
	if m == nil || local == nil {
		return
	}

	if m.Type.Method != stun.MethodBinding ||
		!(m.Type.Class == stun.ClassSuccessResponse ||
			m.Type.Class == stun.ClassRequest ||
			m.Type.Class == stun.ClassIndication) {
		a.log.Tracef("unhandled STUN from %s to %s class(%s) method(%s)", remote, local, m.Type.Class, m.Type.Method)
		return
	}

	if a.isControlling {
		if m.Contains(stun.AttrICEControlling) {
			a.log.Debug("inbound isControlling && a.isControlling == true")
			return
		} else if m.Contains(stun.AttrUseCandidate) {
			a.log.Debug("useCandidate && a.isControlling == true")
			return
		}
	} else {
		if m.Contains(stun.AttrICEControlled) {
			a.log.Debug("inbound isControlled && a.isControlling == false")
			return
		}
	}

	remoteCandidate := a.findRemoteCandidate(local.NetworkType(), remote)
	if m.Type.Class == stun.ClassSuccessResponse {
		if err = assertInboundMessageIntegrity(m, []byte(a.remotePwd)); err != nil {
			a.log.Warnf("discard message from (%s), %v", remote, err)
			return
		}

		if remoteCandidate == nil {
			a.log.Warnf("discard success message from (%s), no such remote", remote)
			return
		}

		a.selector.HandleSuccessResponse(m, local, remoteCandidate, remote)
	} else if m.Type.Class == stun.ClassRequest {
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

			prflxCandidateConfig := CandidatePeerReflexiveConfig{
				Network:   networkType.String(),
				Address:   ip.String(),
				Port:      port,
				Component: local.Component(),
				RelAddr:   "",
				RelPort:   0,
			}

			prflxCandidate, err := NewCandidatePeerReflexive(&prflxCandidateConfig)
			if err != nil {
				a.log.Errorf("Failed to create new remote prflx candidate (%s)", err)
				return
			}
			remoteCandidate = prflxCandidate

			a.log.Debugf("adding a new peer-reflexive candidate: %s ", remote)
			a.addRemoteCandidate(remoteCandidate)
		}

		a.log.Tracef("inbound STUN (Request) from %s to %s", remote.String(), local.String())

		a.selector.HandleBindingRequest(m, local, remoteCandidate)
	}

	if remoteCandidate != nil {
		remoteCandidate.seen(false)
	}
}

// validateNonSTUNTraffic processes non STUN traffic from a remote candidate,
// and returns true if it is an actual remote candidate
func (a *Agent) validateNonSTUNTraffic(local Candidate, remote net.Addr) bool {
	var isValidCandidate uint64
	if err := a.run(func(agent *Agent) {
		remoteCandidate := a.findRemoteCandidate(local.NetworkType(), remote)
		if remoteCandidate != nil {
			remoteCandidate.seen(false)
			atomic.AddUint64(&isValidCandidate, 1)
		}
	}, nil); err != nil {
		a.log.Warnf("failed to validate remote candidate: %v", err)
	}

	return atomic.LoadUint64(&isValidCandidate) == 1
}

func (a *Agent) getSelectedPair() *candidatePair {
	selectedPair := a.selectedPair.Load()

	if selectedPair == nil {
		return nil
	}

	return selectedPair.(*candidatePair)
}

func (a *Agent) closeMulticastConn() {
	if a.mDNSConn != nil {
		if err := a.mDNSConn.Close(); err != nil {
			a.log.Warnf("failed to close mDNS Conn: %v", err)
		}
	}
}

// SetRemoteCredentials sets the credentials of the remote agent
func (a *Agent) SetRemoteCredentials(remoteUfrag, remotePwd string) error {
	switch {
	case remoteUfrag == "":
		return ErrRemoteUfragEmpty
	case remotePwd == "":
		return ErrRemotePwdEmpty
	}

	return a.run(func(agent *Agent) {
		agent.remoteUfrag = remoteUfrag
		agent.remotePwd = remotePwd
	}, nil)
}

// Restart restarts the ICE Agent with the provided ufrag/pwd
// If no ufrag/pwd is provided the Agent will generate one itself
//
// Restart must only be called when GatheringState is GatheringStateComplete
// a user must then call GatherCandidates explicitly to start generating new ones
func (a *Agent) Restart(ufrag, pwd string) error {
	if ufrag == "" {
		var err error
		ufrag, err = generateUFrag()
		if err != nil {
			return err
		}
	}
	if pwd == "" {
		var err error
		pwd, err = generatePwd()
		if err != nil {
			return err
		}
	}

	if len([]rune(ufrag))*8 < 24 {
		return ErrLocalUfragInsufficientBits
	}
	if len([]rune(pwd))*8 < 128 {
		return ErrLocalPwdInsufficientBits
	}

	err := make(chan error, 1)
	if runErr := a.run(func(agent *Agent) {
		if agent.gatheringState == GatheringStateGathering {
			err <- ErrRestartWhenGathering
			return
		}

		// Clear all agent needed to take back to fresh state
		agent.localUfrag = ufrag
		agent.localPwd = pwd
		agent.remoteUfrag = ""
		agent.remotePwd = ""
		a.gatheringState = GatheringStateNew
		a.checklist = make([]*candidatePair, 0)
		a.pendingBindingRequests = make([]bindingRequest, 0)
		a.setSelectedPair(nil)
		a.deleteAllCandidates()
		if a.selector != nil {
			a.selector.Start()
		}

		// Restart is used by NewAgent. Accept/Connect should be used to move to checking
		// for new Agents
		if a.connectionState != ConnectionStateNew {
			a.updateConnectionState(ConnectionStateChecking)
		}

		close(err)
	}, nil); runErr != nil {
		return runErr
	}
	return <-err
}

func (a *Agent) setGatheringState(newState GatheringState) error {
	done := make(chan struct{})
	if err := a.run(func(agent *Agent) {
		if a.gatheringState != newState && newState == GatheringStateComplete {
			a.chanCandidate <- nil
		}

		a.gatheringState = newState
		close(done)
	}, nil); err != nil {
		return err
	}

	<-done
	return nil
}
