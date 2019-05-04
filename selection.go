package ice

import (
	"net"

	"github.com/pion/logging"
	"github.com/pion/stun"
)

type pairCandidateSelector interface {
	Start()
	ContactCandidates()
	PingCandidate(local, remote *Candidate)
	HandleSucessResponse(m *stun.Message, local, remote *Candidate, remoteAddr net.Addr)
	HandleBindingRequest(m *stun.Message, local, remote *Candidate)
}

type controllingSelector struct {
	agent         *Agent
	nominatedPair *candidatePair
	log           logging.LeveledLogger
}

func (s *controllingSelector) Start() {
}

func (s *controllingSelector) ContactCandidates() {
	switch {
	case s.agent.selectedPair != nil:
		if s.agent.validateSelectedPair() {
			s.log.Trace("checking keepalive")
			s.agent.checkKeepalive()
		}
	case s.nominatedPair != nil:
		s.nominatePair(s.nominatedPair)
	default:
		s.log.Trace("pinging all candidates")
		s.agent.pingAllCandidates()
	}
}

func (s *controllingSelector) nominatePair(pair *candidatePair) {
	transactionID := stun.GenerateTransactionID()

	// The controlling agent MUST include the USE-CANDIDATE attribute in
	// order to nominate a candidate pair (Section 8.1.1).  The controlled
	// agent MUST NOT include the USE-CANDIDATE attribute in a Binding
	// request.
	msg, err := stun.Build(stun.ClassRequest, stun.MethodBinding, transactionID,
		&stun.Username{Username: s.agent.remoteUfrag + ":" + s.agent.localUfrag},
		&stun.UseCandidate{},
		&stun.IceControlling{TieBreaker: s.agent.tieBreaker},
		&stun.Priority{Priority: pair.local.Priority()},
		&stun.MessageIntegrity{
			Key: []byte(s.agent.remotePwd),
		},
		&stun.Fingerprint{},
	)

	if err != nil {
		s.log.Error(err.Error())
		return
	}

	s.log.Tracef("ping STUN (nominate candidate pair) from %s to %s\n", pair.local.String(), pair.remote.String())
	s.agent.sendBindingRequest(msg, pair.local, pair.remote)
}

func (s *controllingSelector) HandleBindingRequest(m *stun.Message, local, remote *Candidate) {
	s.agent.sendBindingSuccess(m, local, remote)

	p := s.agent.findValidPair(local, remote)
	if p != nil && s.nominatedPair == nil && s.agent.selectedPair == nil {
		s.nominatedPair = p
		s.nominatePair(p)
	}
}

func (s *controllingSelector) HandleSucessResponse(m *stun.Message, local, remote *Candidate, remoteAddr net.Addr) {
	ok, pendingRequest := s.agent.handleInboundBindingSuccess(m.TransactionID)
	if !ok {
		s.log.Errorf("discard message from (%s), invalid TransactionID %s", remote, m.TransactionID)
		return
	}

	transactionAddr := pendingRequest.destination

	// Assert that NAT is not symmetric
	// https://tools.ietf.org/html/rfc8445#section-7.2.5.2.1
	if !addrEqual(transactionAddr, remoteAddr) {
		s.log.Debugf("discard message: transaction source and destination does not match expected(%s), actual(%s)", transactionAddr, remote)
		return
	}

	s.log.Tracef("inbound STUN (SuccessResponse) from %s to %s", remote.String(), local.String())
	p := s.agent.addValidPair(local, remote)

	if pendingRequest.isUseCandidate && s.agent.selectedPair == nil {
		s.agent.setSelectedPair(p)
	}
}

func (s *controllingSelector) PingCandidate(local, remote *Candidate) {
	transactionID := stun.GenerateTransactionID()

	msg, err := stun.Build(stun.ClassRequest, stun.MethodBinding, transactionID,
		&stun.Username{Username: s.agent.remoteUfrag + ":" + s.agent.localUfrag},
		&stun.IceControlling{TieBreaker: s.agent.tieBreaker},
		&stun.Priority{Priority: local.Priority()},
		&stun.MessageIntegrity{
			Key: []byte(s.agent.remotePwd),
		},
		&stun.Fingerprint{},
	)

	if err != nil {
		s.log.Error(err.Error())
		return
	}

	s.agent.sendBindingRequest(msg, local, remote)
}

type controlledSelector struct {
	agent *Agent
	log   logging.LeveledLogger
}

func (s *controlledSelector) Start() {}

func (s *controlledSelector) ContactCandidates() {
	if s.agent.selectedPair != nil {
		if s.agent.validateSelectedPair() {
			s.log.Trace("checking keepalive")
			s.agent.checkKeepalive()
		}
	} else {
		s.log.Trace("pinging all candidates")
		s.agent.pingAllCandidates()
	}
}

func (s *controlledSelector) PingCandidate(local, remote *Candidate) {
	transactionID := stun.GenerateTransactionID()

	msg, err := stun.Build(stun.ClassRequest, stun.MethodBinding, transactionID,
		&stun.Username{Username: s.agent.remoteUfrag + ":" + s.agent.localUfrag},
		&stun.IceControlled{TieBreaker: s.agent.tieBreaker},
		&stun.Priority{Priority: local.Priority()},
		&stun.MessageIntegrity{
			Key: []byte(s.agent.remotePwd),
		},
		&stun.Fingerprint{},
	)

	if err != nil {
		s.log.Error(err.Error())
		return
	}

	s.agent.sendBindingRequest(msg, local, remote)
}

func (s *controlledSelector) HandleSucessResponse(m *stun.Message, local, remote *Candidate, remoteAddr net.Addr) {
	// TODO according to the standard we should specifically answer a failed nomination:
	// https://tools.ietf.org/html/rfc8445#section-7.3.1.5
	// If the controlled agent does not accept the request from the
	// controlling agent, the controlled agent MUST reject the nomination
	// request with an appropriate error code response (e.g., 400)
	// [RFC5389].

	ok, pendingRequest := s.agent.handleInboundBindingSuccess(m.TransactionID)
	if !ok {
		s.log.Errorf("discard message from (%s), invalid TransactionID %s", remote, m.TransactionID)
		return
	}

	transactionAddr := pendingRequest.destination

	// Assert that NAT is not symmetric
	// https://tools.ietf.org/html/rfc8445#section-7.2.5.2.1
	if !addrEqual(transactionAddr, remoteAddr) {
		s.log.Debugf("discard message: transaction source and destination does not match expected(%s), actual(%s)", transactionAddr, remote)
		return
	}

	s.log.Tracef("inbound STUN (SuccessResponse) from %s to %s", remote.String(), local.String())
	s.agent.addValidPair(local, remote)
}

func (s *controlledSelector) HandleBindingRequest(m *stun.Message, local, remote *Candidate) {
	_, useCandidate := m.GetOneAttribute(stun.AttrUseCandidate)

	if useCandidate {
		// https://tools.ietf.org/html/rfc8445#section-7.3.1.5
		p := s.agent.findValidPair(local, remote)

		if p != nil {
			// If the state of this pair is Succeeded, it means that the check
			// previously sent by this pair produced a successful response and
			// generated a valid pair (Section 7.2.5.3.2).  The agent sets the
			// nominated flag value of the valid pair to true.
			if s.agent.selectedPair == nil {
				s.agent.setSelectedPair(p)
			}
			s.agent.sendBindingSuccess(m, local, remote)
		} else {
			// If the received Binding request triggered a new check to be
			// enqueued in the triggered-check queue (Section 7.3.1.4), once the
			// check is sent and if it generates a successful response, and
			// generates a valid pair, the agent sets the nominated flag of the
			// pair to true.  If the request fails (Section 7.2.5.2), the agent
			// MUST remove the candidate pair from the valid list, set the
			// candidate pair state to Failed, and set the checklist state to
			// Failed.
			s.PingCandidate(local, remote)
		}
	} else {
		s.agent.sendBindingSuccess(m, local, remote)
		s.PingCandidate(local, remote)
	}
}
