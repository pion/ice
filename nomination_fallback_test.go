// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/stretchr/testify/require"
)

// newFallbackTestAgent returns a bare controlling agent with a controllingSelector,
// ready to drive directly (no sockets, no goroutines).
func newFallbackTestAgent(t *testing.T) (*Agent, *controllingSelector) {
	t.Helper()

	agent := bareAgentForPing()
	agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
	agent.remoteUfrag = selectionTestRemoteUfrag
	agent.localUfrag = selectionTestLocalUfrag
	agent.remotePwd = selectionTestPassword
	agent.localPwd = selectionTestPassword
	agent.tieBreaker = 1
	agent.maxBindingRequests = 7
	agent.hostAcceptanceMinWait = 0
	agent.prflxAcceptanceMinWait = 0
	agent.isControlling.Store(true)
	agent.onConnected = make(chan struct{})
	agent.setSelector()

	selector, ok := agent.getSelector().(*controllingSelector)
	require.True(t, ok)
	selector.Start()

	return agent, selector
}

func fallbackTestCand(ip string, port int) *pingNoIOCand {
	c := newPingNoIOCand()
	c.candidateBase.networkType = NetworkTypeUDP4
	c.candidateBase.setResolvedAddr(&net.UDPAddr{IP: net.ParseIP(ip), Port: port})

	return c
}

// fallbackTestSrflxCand has a lower type preference than a host candidate, so its
// pair always has lower priority regardless of address.
func fallbackTestSrflxCand(ip string, port int) *pingNoIOCand {
	c := fallbackTestCand(ip, port)
	c.candidateBase.candidateType = CandidateTypeServerReflexive

	return c
}

// lastPendingBindingRequest returns a copy of the most recently sent, still-pending
// binding request.
func lastPendingBindingRequest(t *testing.T, agent *Agent) bindingRequest {
	t.Helper()
	require.NotEmpty(t, agent.pendingBindingRequests)

	return agent.pendingBindingRequests[len(agent.pendingBindingRequests)-1]
}

func buildSuccessResponse(t *testing.T, agent *Agent, transactionID [stun.TransactionIDSize]byte) *stun.Message {
	t.Helper()
	msg, err := stun.Build(stun.NewTransactionIDSetter(transactionID), stun.BindingSuccess,
		stun.NewShortTermIntegrity(agent.remotePwd), stun.Fingerprint)
	require.NoError(t, err)

	return msg
}

// An asymmetric nomination response fails the pair immediately and nominates
// another valid one, instead of resending the same doomed nomination forever.
func TestControllingSelector_FallsBackFromAsymmetricNominationResponse(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	stale := fallbackTestCand("10.24.0.236", 61883) // peer's old source
	fresh := fallbackTestCand("10.24.0.30", 61883)  // peer's current source
	stalePair := agent.addPair(local, stale)
	stalePair.state = CandidatePairStateSucceeded
	freshPair := agent.addPair(local, fresh)
	freshPair.state = CandidatePairStateSucceeded

	// Nominate the first valid pair; its response comes from the peer's other address.
	selector.ContactCandidates()
	require.Equal(t, stalePair, selector.nominatedPair)
	nominationReq := lastPendingBindingRequest(t, agent)
	resp := buildSuccessResponse(t, agent, nominationReq.transactionID)

	selector.HandleSuccessResponse(resp, local, fresh, fresh.addrPort())

	// RFC 8445 Section 7.2.5.2.1: the asymmetric response immediately fails the pair.
	require.Nil(t, agent.getSelectedPair())
	require.Nil(t, selector.nominatedPair)
	require.Equal(t, CandidatePairStateFailed, stalePair.state)
	require.False(t, stalePair.nominated)

	// The next tick nominates the only remaining valid pair, and a symmetric response
	// selects it normally.
	selector.ContactCandidates()
	require.Equal(t, freshPair, selector.nominatedPair)
	freshReq := lastPendingBindingRequest(t, agent)
	selector.HandleSuccessResponse(buildSuccessResponse(t, agent, freshReq.transactionID), local, fresh, fresh.addrPort())
	require.Equal(t, freshPair, agent.getSelectedPair())
}

// With no alternative pair, an asymmetric response must not strand the sole pair;
// the nomination stays in place and keeps retrying.
func TestControllingSelector_KeepsRetryingSoleNominationOnAsymmetricResponse(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	stale := fallbackTestCand("10.24.0.236", 61883)
	fresh := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, stale)
	pair.state = CandidatePairStateSucceeded

	selector.ContactCandidates()
	require.Equal(t, pair, selector.nominatedPair)
	req := lastPendingBindingRequest(t, agent)

	selector.HandleSuccessResponse(buildSuccessResponse(t, agent, req.transactionID), local, fresh, fresh.addrPort())

	require.Equal(t, pair, selector.nominatedPair, "sole pair must not be abandoned with no alternative to fail over to")
	require.Equal(t, CandidatePairStateSucceeded, pair.state)
	require.Nil(t, agent.getSelectedPair())

	// The next tick simply retries the same nomination.
	selector.ContactCandidates()
	require.Equal(t, pair, selector.nominatedPair)
}

// Any of the abandoned pair's other pending requests, not just the one that
// triggered the fallback, must have their responses discarded too.
func TestControllingSelector_DiscardsStaleResponsesForAbandonedPair(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	stale := fallbackTestCand("10.24.0.236", 61883)
	fresh := fallbackTestCand("10.24.0.30", 61883)
	stalePair := agent.addPair(local, stale)
	stalePair.state = CandidatePairStateSucceeded
	freshPair := agent.addPair(local, fresh)
	freshPair.state = CandidatePairStateSucceeded

	// A plain connectivity check for stalePair, still outstanding alongside its
	// nomination (e.g. a keepalive, or a retry from before it first succeeded).
	selector.PingCandidate(local, stale)
	plainReq := lastPendingBindingRequest(t, agent)
	require.False(t, plainReq.isUseCandidate)

	// Two ticks' worth of nomination retries for stalePair before any response
	// arrives.
	selector.ContactCandidates()
	require.Equal(t, stalePair, selector.nominatedPair)
	firstNominationReq := lastPendingBindingRequest(t, agent)
	selector.ContactCandidates()
	secondNominationReq := lastPendingBindingRequest(t, agent)
	require.NotEqual(t, firstNominationReq.transactionID, secondNominationReq.transactionID)

	// The response to the most recent nomination request is asymmetric: stalePair is
	// abandoned in favor of freshPair.
	resp := buildSuccessResponse(t, agent, secondNominationReq.transactionID)
	selector.HandleSuccessResponse(resp, local, fresh, fresh.addrPort())
	require.Equal(t, CandidatePairStateFailed, stalePair.state)
	require.NotZero(t, stalePair.nominationAbandonedSeq)
	require.Nil(t, selector.nominatedPair)

	selector.ContactCandidates()
	require.Equal(t, freshPair, selector.nominatedPair)
	freshReq := lastPendingBindingRequest(t, agent)
	selector.HandleSuccessResponse(buildSuccessResponse(t, agent, freshReq.transactionID), local, fresh, fresh.addrPort())
	require.Equal(t, freshPair, agent.getSelectedPair())

	// Neither stalePair's still-unanswered first nomination transaction nor its plain
	// check may revive it now.
	for _, req := range []bindingRequest{firstNominationReq, plainReq} {
		selector.HandleSuccessResponse(buildSuccessResponse(t, agent, req.transactionID), local, stale, stale.addrPort())
	}
	require.Equal(t, CandidatePairStateFailed, stalePair.state, "no stale response may resurrect the abandoned pair")
	require.Equal(t, freshPair, agent.getSelectedPair())
	require.Equal(t, freshPair, selector.nominatedPair)
}

// A different pair sharing the abandoned pair's remote candidate must be
// unaffected: abandonment is per pair, not per address.
func TestControllingSelector_DifferentPairSharingRemoteIsUnaffected(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	localA := fallbackTestCand("10.24.0.236", 60170)
	localB := fallbackTestCand("10.24.0.50", 60171)
	stale := fallbackTestCand("10.24.0.236", 61883)
	fresh := fallbackTestCand("10.24.0.30", 61883)

	abandonedPair := agent.addPair(localA, stale)
	abandonedPair.state = CandidatePairStateSucceeded
	freshPair := agent.addPair(localA, fresh)
	freshPair.state = CandidatePairStateSucceeded

	// A different pair to the exact same remote candidate as the one about to be
	// abandoned, with its own pending plain check.
	otherPair := agent.addPair(localB, stale)
	otherPair.state = CandidatePairStateInProgress
	selector.PingCandidate(localB, stale)
	otherReq := lastPendingBindingRequest(t, agent)

	selector.ContactCandidates()
	require.Equal(t, abandonedPair, selector.nominatedPair)
	nominationReq := lastPendingBindingRequest(t, agent)
	nominationResp := buildSuccessResponse(t, agent, nominationReq.transactionID)
	selector.HandleSuccessResponse(nominationResp, localA, fresh, fresh.addrPort())
	require.Equal(t, CandidatePairStateFailed, abandonedPair.state)
	require.NotZero(t, abandonedPair.nominationAbandonedSeq)
	require.Zero(t, otherPair.nominationAbandonedSeq, "a different pair must not be flagged")

	// otherPair's own pending check succeeds completely normally.
	selector.HandleSuccessResponse(buildSuccessResponse(t, agent, otherReq.transactionID), localB, stale, stale.addrPort())
	require.Equal(t, CandidatePairStateSucceeded, otherPair.state,
		"a different pair sharing the abandoned pair's remote must be unaffected")
}

// Ordinary retry exhaustion, unrelated to the asymmetric-nomination fallback, must
// still be recoverable by a late, valid response.
func TestControllingSelector_NormalExhaustionRecoversFromLateResponse(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateInProgress

	// An earlier check, still pending (well within its 4s expiry) when the pair is
	// later failed by ordinary retry exhaustion.
	selector.PingCandidate(local, remote)
	earlierReq := lastPendingBindingRequest(t, agent)

	// Exhaust the pair via the normal pingAllCandidates path.
	pair.bindingRequestCount = agent.maxBindingRequests + 1
	agent.pingAllCandidates()
	require.Equal(t, CandidatePairStateFailed, pair.state)
	require.Zero(t, pair.nominationAbandonedSeq, "only the asymmetric-nomination fallback may set this")

	// A late, valid response to the earlier, still-pending check must still recover
	// the pair.
	earlierResp := buildSuccessResponse(t, agent, earlierReq.transactionID)
	selector.HandleSuccessResponse(earlierResp, local, remote, remote.addrPort())
	require.Equal(t, CandidatePairStateSucceeded, pair.state,
		"normal retry exhaustion must still be recoverable by a late valid response")
}

// A late response to a pre-abandonment request must not revive the pair even after
// re-arm; only a response to a post-abandonment request, like the triggered check
// itself, is accepted.
func TestControllingSelector_ReArmAcceptsOnlyResponsesSentAfterAbandonment(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	stale := fallbackTestCand("10.24.0.236", 61883)
	fresh := fallbackTestCand("10.24.0.30", 61883)
	stalePair := agent.addPair(local, stale)
	stalePair.state = CandidatePairStateSucceeded
	freshPair := agent.addPair(local, fresh)
	freshPair.state = CandidatePairStateSucceeded

	// An older nomination retry for stalePair, sent before it is abandoned and left
	// permanently unanswered.
	selector.ContactCandidates()
	require.Equal(t, stalePair, selector.nominatedPair)
	oldReq := lastPendingBindingRequest(t, agent)

	// A second retry is what actually gets the asymmetric response that abandons the
	// pair.
	selector.ContactCandidates()
	abandoningReq := lastPendingBindingRequest(t, agent)
	require.NotEqual(t, oldReq.transactionID, abandoningReq.transactionID)
	abandoningResp := buildSuccessResponse(t, agent, abandoningReq.transactionID)
	selector.HandleSuccessResponse(abandoningResp, local, fresh, fresh.addrPort())
	require.Equal(t, CandidatePairStateFailed, stalePair.state)
	require.NotZero(t, stalePair.nominationAbandonedSeq)

	// The peer later sends a Binding request for the abandoned pair: re-arm sets it
	// back to Waiting and sends its own triggered check.
	peerReq, err := stun.Build(stun.BindingRequest, stun.TransactionID,
		stun.NewShortTermIntegrity(selectionTestPassword), stun.Fingerprint)
	require.NoError(t, err)
	selector.HandleBindingRequest(peerReq, local, stale)
	require.Equal(t, CandidatePairStateWaiting, stalePair.state)
	triggered := lastPendingBindingRequest(t, agent)
	require.False(t, triggered.isUseCandidate)

	// A late response to the OLD, pre-abandonment nomination retry must still be
	// discarded, even though the pair is Waiting again and no longer Failed.
	oldResp := buildSuccessResponse(t, agent, oldReq.transactionID)
	selector.HandleSuccessResponse(oldResp, local, stale, stale.addrPort())
	require.Equal(t, CandidatePairStateWaiting, stalePair.state,
		"a response to a pre-abandonment transaction must still be discarded")
	require.Nil(t, agent.getSelectedPair())

	// The triggered check's own (post-abandonment) success must be accepted.
	triggeredResp := buildSuccessResponse(t, agent, triggered.transactionID)
	selector.HandleSuccessResponse(triggeredResp, local, stale, stale.addrPort())
	require.Equal(t, CandidatePairStateSucceeded, stalePair.state, "the re-arm's triggered check success must be accepted")
}

// A stale retry from a previously abandoned pair B must not be attributed to a
// different pair A's current nomination, even when B and A share a remote
// candidate and the retry's response arrives on A's local candidate.
func TestControllingSelector_StaleRetryFromAbandonedPairDoesNotFailCurrentNomination(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	localB := fallbackTestCand("10.24.0.10", 60170)
	localA := fallbackTestCand("10.24.0.20", 60171)
	sharedRemote := fallbackTestCand("10.24.1.5", 61883) // B and A's remote candidate
	freshForB := fallbackTestCand("10.24.1.6", 61884)

	pairB := agent.addPair(localB, sharedRemote)
	pairB.state = CandidatePairStateSucceeded
	altForB := agent.addPair(localB, freshForB)
	altForB.state = CandidatePairStateSucceeded

	// Nominate B; its first retry is left permanently unanswered.
	selector.ContactCandidates()
	require.Equal(t, pairB, selector.nominatedPair)
	staleReqFromB := lastPendingBindingRequest(t, agent)

	// A second retry receives the asymmetric response that abandons B.
	selector.ContactCandidates()
	abandoningReq := lastPendingBindingRequest(t, agent)
	require.NotEqual(t, staleReqFromB.transactionID, abandoningReq.transactionID)
	abandoningResp := buildSuccessResponse(t, agent, abandoningReq.transactionID)
	selector.HandleSuccessResponse(abandoningResp, localB, freshForB, freshForB.addrPort())
	require.Equal(t, CandidatePairStateFailed, pairB.state)

	// Pair A, to the exact same remote candidate as B, is now the current nomination
	// (standing in for ContactCandidates picking a different pair on a later tick).
	// nominatedSeq is set to what the next request sent would be assigned, exactly
	// as controllingSelector.nominate does for a real nomination.
	pairA := agent.addPair(localA, sharedRemote)
	pairA.state = CandidatePairStateSucceeded
	selector.nominatedPair = pairA
	selector.nominatedSeq = agent.bindingRequestSeq.Load() + 1

	// staleReqFromB, sent before A was nominated, gets a response on A's local candidate.
	staleResp := buildSuccessResponse(t, agent, staleReqFromB.transactionID)
	selector.HandleSuccessResponse(staleResp, localA, freshForB, freshForB.addrPort())

	require.Equal(t, pairA, selector.nominatedPair, "a retry predating A's nomination must not be attributed to it")
	require.Equal(t, CandidatePairStateSucceeded, pairA.state)
	require.Zero(t, pairA.nominationAbandonedSeq)
}

// The role-conflict (487) retry path re-arms a pair like an ordinary re-arm,
// without touching nominationAbandonedSeq; a later response is still accepted.
func TestControllingSelector_RoleConflictRetryAcceptsLaterSuccessForAbandonedPair(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	stale := fallbackTestCand("10.24.0.236", 61883)
	fresh := fallbackTestCand("10.24.0.30", 61883)
	stalePair := agent.addPair(local, stale)
	stalePair.state = CandidatePairStateSucceeded
	freshPair := agent.addPair(local, fresh)
	freshPair.state = CandidatePairStateSucceeded

	// Abandon stalePair as usual.
	selector.ContactCandidates()
	require.Equal(t, stalePair, selector.nominatedPair)
	nominationReq := lastPendingBindingRequest(t, agent)
	abandoningResp := buildSuccessResponse(t, agent, nominationReq.transactionID)
	selector.HandleSuccessResponse(abandoningResp, local, fresh, fresh.addrPort())
	require.Equal(t, CandidatePairStateFailed, stalePair.state)
	require.NotZero(t, stalePair.nominationAbandonedSeq)

	// A 487 role-conflict error re-arms the pair without clearing the cutoff.
	selector.PingCandidate(local, stale)
	plainReq := lastPendingBindingRequest(t, agent)
	errMsg, err := stun.Build(stun.NewTransactionIDSetter(plainReq.transactionID), stun.BindingError,
		stun.ErrorCodeAttribute{Code: stun.CodeRoleConflict, Reason: []byte("Role Conflict")},
		stun.NewShortTermIntegrity(agent.remotePwd), stun.Fingerprint)
	require.NoError(t, err)
	handled := agent.handleInboundErrorResponse(stale, local, stale.addrPort(), errMsg)
	require.True(t, handled)
	require.Equal(t, CandidatePairStateWaiting, stalePair.state)
	require.NotZero(t, stalePair.nominationAbandonedSeq, "the 487 path does not clear nominationAbandonedSeq")

	// A later, valid response must still be accepted.
	selector.PingCandidate(local, stale)
	laterReq := lastPendingBindingRequest(t, agent)
	laterResp := buildSuccessResponse(t, agent, laterReq.transactionID)
	selector.HandleSuccessResponse(laterResp, local, stale, stale.addrPort())
	require.Equal(t, CandidatePairStateSucceeded, stalePair.state, "a later valid response must be accepted after the 487 retry")
}

// A nomination that never gets a response must not be abandoned by tick or retry
// counting; resending it forever is intentional.
func TestControllingSelector_NominationWithoutResponseIsNotAbandoned(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateSucceeded

	selector.ContactCandidates()
	require.Equal(t, pair, selector.nominatedPair)

	// Comfortably past the old maxBindingRequests-based cutoff (7), and far more
	// ticks than a real nomination round trip would ever take.
	for i := range 50 {
		selector.ContactCandidates()
		require.Equal(t, pair, selector.nominatedPair, "tick %d: nomination must not be abandoned without a response", i)
		require.Equal(t, CandidatePairStateSucceeded, pair.state)
	}
}

// A Binding request for a Failed pair re-arms it and triggers exactly one new
// connectivity check per re-arm.
func TestControllingSelector_ReArmsFailedPairWithOneTriggeredCheck(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateFailed
	pair.bindingRequestCount = 9

	req, err := stun.Build(stun.BindingRequest, stun.TransactionID,
		stun.NewShortTermIntegrity(selectionTestPassword), stun.Fingerprint)
	require.NoError(t, err)

	before := len(agent.pendingBindingRequests)
	selector.HandleBindingRequest(req, local, remote)

	require.Equal(t, CandidatePairStateWaiting, pair.state)
	require.Zero(t, pair.bindingRequestCount)
	require.Len(t, agent.pendingBindingRequests, before+1, "re-arm must send exactly one triggered check")
	triggered := agent.pendingBindingRequests[len(agent.pendingBindingRequests)-1]
	require.False(t, triggered.isUseCandidate, "the triggered check is a plain connectivity check, not a nomination")

	// A second Binding request for the same (now Waiting, not Failed) pair must not
	// send another check from this path.
	req2, err := stun.Build(stun.BindingRequest, stun.TransactionID,
		stun.NewShortTermIntegrity(selectionTestPassword), stun.Fingerprint)
	require.NoError(t, err)
	afterFirst := len(agent.pendingBindingRequests)
	selector.HandleBindingRequest(req2, local, remote)
	require.Len(t, agent.pendingBindingRequests, afterFirst)

	// Once a pair is selected, a Failed pair must not be re-armed at all.
	selectedLocal := fallbackTestCand("10.24.0.236", 60171)
	selectedRemote := fallbackTestCand("10.24.0.31", 61884)
	selectedPair := agent.addPair(selectedLocal, selectedRemote)
	selectedPair.state = CandidatePairStateSucceeded
	agent.setSelectedPair(selectedPair)

	otherLocal := fallbackTestCand("10.24.0.236", 60172)
	otherRemote := fallbackTestCand("10.24.0.32", 61885)
	otherFailed := agent.addPair(otherLocal, otherRemote)
	otherFailed.state = CandidatePairStateFailed

	req3, err := stun.Build(stun.BindingRequest, stun.TransactionID,
		stun.NewShortTermIntegrity(selectionTestPassword), stun.Fingerprint)
	require.NoError(t, err)
	beforePostSelection := len(agent.pendingBindingRequests)
	selector.HandleBindingRequest(req3, otherLocal, otherRemote)
	require.Equal(t, CandidatePairStateFailed, otherFailed.state, "must not resurrect a Failed pair once selected")
	require.Len(t, agent.pendingBindingRequests, beforePostSelection)
}

// If the single best alternative pair is still behind its acceptance wait, the
// fallback must not abandon the current pair for a lower-priority one that happens
// to be nominatable now: the next tick wouldn't nominate it either.
func TestControllingSelector_NoFailoverWhenBestAlternativeNotYetNominatable(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	stale := fallbackTestCand("10.24.0.236", 61883)
	fresh := fallbackTestCand("10.24.0.30", 61883)
	stalePair := agent.addPair(local, stale)
	stalePair.state = CandidatePairStateSucceeded

	// Nominate stalePair while the acceptance wait is still zero for everything.
	selector.ContactCandidates()
	require.Equal(t, stalePair, selector.nominatedPair)
	req := lastPendingBindingRequest(t, agent)

	// A higher-priority host pair is gated behind a real wait; a lower-priority
	// srflx pair has none. ContactCandidates only ever considers the best pair,
	// so it would find the host pair not yet nominatable and stop there.
	agent.hostAcceptanceMinWait = time.Hour
	agent.srflxAcceptanceMinWait = 0
	hostAlt := agent.addPair(fallbackTestCand("10.24.0.236", 60171), fallbackTestCand("10.24.0.40", 61884))
	hostAlt.state = CandidatePairStateSucceeded

	srflxAlt := agent.addPair(fallbackTestSrflxCand("10.24.0.236", 60172), fallbackTestSrflxCand("10.24.0.41", 61885))
	srflxAlt.state = CandidatePairStateSucceeded

	selector.HandleSuccessResponse(buildSuccessResponse(t, agent, req.transactionID), local, fresh, fresh.addrPort())

	require.Equal(t, stalePair, selector.nominatedPair,
		"must not fail over: the best alternative by priority is not yet nominatable, matching what the next tick would do")
	require.Equal(t, CandidatePairStateSucceeded, stalePair.state)
}

// A renomination for a different pair B must not be attributed to pair A's current
// nomination, even when B and A share a remote candidate and B's asymmetric
// response arrives on A's local candidate. nominationValue can't distinguish them:
// with no value generator configured, a renomination carries no nomination
// attribute either.
func TestControllingSelector_RenominationAsymmetricResponseDoesNotFailCurrentNomination(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)
	agent.enableRenomination = true

	localA := fallbackTestCand("10.24.0.20", 60171)
	localB := fallbackTestCand("10.24.0.10", 60170)
	sharedRemote := fallbackTestCand("10.24.1.5", 61883)
	freshForB := fallbackTestCand("10.24.1.6", 61884)

	pairA := agent.addPair(localA, sharedRemote)
	pairA.state = CandidatePairStateSucceeded
	pairB := agent.addPair(localB, sharedRemote)
	pairB.state = CandidatePairStateSucceeded

	// Nominate A through the normal path.
	selector.ContactCandidates()
	require.Equal(t, pairA, selector.nominatedPair)

	// While A is the current nomination, renominate B via the public API.
	require.NoError(t, agent.RenominateCandidate(localB, sharedRemote))
	renominationReq := lastPendingBindingRequest(t, agent)
	require.True(t, renominationReq.isUseCandidate)
	require.Nil(t, renominationReq.nominationValue,
		"with no value generator configured, a renomination request carries no nomination attribute either")
	require.False(t, renominationReq.isPrimaryNomination, "a renomination request is not a primary nomination")

	// Arrives on A's local candidate while naming A's remote candidate.
	renominationResp := buildSuccessResponse(t, agent, renominationReq.transactionID)
	selector.HandleSuccessResponse(renominationResp, localA, freshForB, freshForB.addrPort())

	require.Equal(t, pairA, selector.nominatedPair,
		"a renomination response for a different pair must not fail the current nomination")
	require.Equal(t, CandidatePairStateSucceeded, pairA.state)
	require.Zero(t, pairA.nominationAbandonedSeq)
}

// time.Now() does not guarantee a distinct value on every call, but seq never ties.
// Two synthetic requests sharing a timestamp but straddling the abandonment seq
// boundary must still be ordered correctly by seq alone.
func TestControllingSelector_EqualTimestampDoesNotCrossSeqBoundary(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	stale := fallbackTestCand("10.24.0.236", 61883)
	fresh := fallbackTestCand("10.24.0.30", 61883)
	stalePair := agent.addPair(local, stale)
	stalePair.state = CandidatePairStateSucceeded
	freshPair := agent.addPair(local, fresh)
	freshPair.state = CandidatePairStateSucceeded

	selector.ContactCandidates()
	require.Equal(t, stalePair, selector.nominatedPair)
	nominationReq := lastPendingBindingRequest(t, agent)
	sameInstant := nominationReq.timestamp

	resp := buildSuccessResponse(t, agent, nominationReq.transactionID)
	selector.HandleSuccessResponse(resp, local, fresh, fresh.addrPort())
	require.Equal(t, CandidatePairStateFailed, stalePair.state)
	boundarySeq := stalePair.nominationAbandonedSeq
	require.NotZero(t, boundarySeq)

	freshTransactionID := func() [stun.TransactionIDSize]byte {
		msg, err := stun.Build(stun.TransactionID)
		require.NoError(t, err)

		return msg.TransactionID
	}

	// A request recorded at the exact same timestamp as the boundary, with seq equal
	// to it, must still be treated as at-or-before: discarded.
	atBoundary := bindingRequest{
		timestamp:     sameInstant,
		transactionID: freshTransactionID(),
		destination:   stale.addrPort(),
		networkType:   stale.NetworkType(),
		seq:           boundarySeq,
	}
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, atBoundary)
	atResp := buildSuccessResponse(t, agent, atBoundary.transactionID)
	selector.HandleSuccessResponse(atResp, local, stale, stale.addrPort())
	require.Equal(t, CandidatePairStateFailed, stalePair.state, "a request at the exact boundary seq must be discarded")

	// A request recorded at the SAME timestamp, but with seq one greater, must be
	// accepted: seq, not the tied timestamp, is what decides it.
	afterBoundary := bindingRequest{
		timestamp:     sameInstant,
		transactionID: freshTransactionID(),
		destination:   stale.addrPort(),
		networkType:   stale.NetworkType(),
		seq:           boundarySeq + 1,
	}
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, afterBoundary)
	afterResp := buildSuccessResponse(t, agent, afterBoundary.transactionID)
	selector.HandleSuccessResponse(afterResp, local, stale, stale.addrPort())
	require.Equal(t, CandidatePairStateSucceeded, stalePair.state,
		"a request with a strictly greater seq, even at an identical timestamp, must be accepted")
}

// freshTransactionIDFor builds a new, valid STUN transaction ID for constructing
// synthetic pending requests directly.
func freshTransactionIDFor(t *testing.T) [stun.TransactionIDSize]byte {
	t.Helper()
	msg, err := stun.Build(stun.TransactionID)
	require.NoError(t, err)

	return msg.TransactionID
}

// nominatedSeq must end up as the sent request's own seq even when an expiring
// pending entry keeps the list's length unchanged.
func TestControllingSelector_ExpiryDuringSendStillCapturesNominatedSeq(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateSucceeded

	// An unrelated, already-expired pending entry: invalidatePendingBindingRequests
	// removes this in the very same call that appends the nomination's own request
	// below, so the list's net length does not change.
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, bindingRequest{
		timestamp:     time.Now().Add(-2 * maxBindingRequestTimeout),
		transactionID: freshTransactionIDFor(t),
		destination:   fallbackTestCand("10.24.9.9", 9).addrPort(),
		networkType:   NetworkTypeUDP4,
	})
	before := len(agent.pendingBindingRequests)

	selector.ContactCandidates()

	require.Equal(t, before, len(agent.pendingBindingRequests),
		"the expired entry's removal must exactly offset the new request's addition")
	require.Equal(t, pair, selector.nominatedPair)
	req := lastPendingBindingRequest(t, agent)
	require.True(t, req.isPrimaryNomination)
	require.NotZero(t, req.seq)
	require.Equal(t, req.seq, selector.nominatedSeq,
		"nominatedSeq must be the new request's own seq even though the pending list didn't grow")
}

// nominatedSeq must not stay stuck at 0 after a failed first attempt; a later
// successful retry must still capture it.
func TestControllingSelector_FirstSendFailureThenRetrySetsNominatedSeq(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateSucceeded

	// Oversized ufrags make stun.NewUsername (and so stun.Build) fail inside
	// nominatePair, exactly as in TestControllingSelector_NominatePair_BuildError.
	longUfrag := strings.Repeat("x", 300)
	agent.remoteUfrag, agent.localUfrag = longUfrag, longUfrag

	selector.ContactCandidates()
	require.Equal(t, pair, selector.nominatedPair, "nominatedPair is assigned before the send is attempted")
	require.Empty(t, agent.pendingBindingRequests, "the failed build must not have sent or recorded anything")
	require.Zero(t, selector.nominatedSeq, "nothing was sent yet, so there is no boundary to record")

	// Restore valid ufrags; the next tick retries the same nomination and this time
	// succeeds.
	agent.remoteUfrag, agent.localUfrag = selectionTestRemoteUfrag, selectionTestLocalUfrag
	selector.ContactCandidates()

	req := lastPendingBindingRequest(t, agent)
	require.True(t, req.isPrimaryNomination)
	require.NotZero(t, req.seq)
	require.Equal(t, req.seq, selector.nominatedSeq,
		"the first successful retry must capture nominatedSeq, not leave it stuck at 0 from the earlier failure")
}

// A role switch installs a new controllingSelector, but pending binding requests
// live on the Agent, not the selector. An asymmetric response to the OLD
// selector's pre-switch request must not abandon the NEW selector's nomination of
// the same pair; seq is a single counter shared across selector instances.
func TestControllingSelector_RoleSwitchOldSelectorPendingNominationDoesNotAbandonNew(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	wrongRemote := fallbackTestCand("10.24.0.99", 61899)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateSucceeded

	// The old (pre-switch) controlling selector nominates pair; its request is left
	// permanently unanswered.
	selector.ContactCandidates()
	require.Equal(t, pair, selector.nominatedPair)
	oldReq := lastPendingBindingRequest(t, agent)
	require.True(t, oldReq.isPrimaryNomination)

	// An unrelated, already-expired entry: renominating pair below purges this in
	// the same call that appends the new request, so the list's length is unchanged.
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, bindingRequest{
		timestamp:     time.Now().Add(-2 * maxBindingRequestTimeout),
		transactionID: freshTransactionIDFor(t),
		destination:   fallbackTestCand("10.24.9.9", 9).addrPort(),
		networkType:   NetworkTypeUDP4,
	})

	// Switch to controlled, then back to controlling: a brand new
	// controllingSelector instance, with its own fresh nominatedPair/nominatedSeq.
	agent.isControlling.Store(false)
	agent.setSelector()
	agent.isControlling.Store(true)
	agent.setSelector()
	newSelector, ok := agent.getSelector().(*controllingSelector)
	require.True(t, ok)
	require.Nil(t, newSelector.nominatedPair)
	require.Zero(t, newSelector.nominatedSeq)

	before := len(agent.pendingBindingRequests)
	newSelector.ContactCandidates()
	require.Equal(t, before, len(agent.pendingBindingRequests),
		"the pre-populated expired entry's removal must offset the new nomination's own addition")
	require.Equal(t, pair, newSelector.nominatedPair)
	newReq := lastPendingBindingRequest(t, agent)
	require.NotEqual(t, oldReq.transactionID, newReq.transactionID)
	require.Equal(t, newReq.seq, newSelector.nominatedSeq,
		"the new selector's nominatedSeq must be its own request's seq despite the list not growing")
	require.Greater(t, newSelector.nominatedSeq, oldReq.seq, "seq is a single counter shared across selector instances")

	// The OLD selector's still-pending, pre-switch request finally gets an
	// asymmetric response. It must not be attributed to the new selector's current
	// nomination of the very same pair.
	staleResp := buildSuccessResponse(t, agent, oldReq.transactionID)
	newSelector.HandleSuccessResponse(staleResp, local, wrongRemote, wrongRemote.addrPort())

	require.Equal(t, pair, newSelector.nominatedPair,
		"a response to the old selector's pre-switch request must not abandon the new nomination")
	require.Equal(t, CandidatePairStateSucceeded, pair.state)
	require.Zero(t, pair.nominationAbandonedSeq)
}

// A paused off-loop renomination call can claim a seq that a concurrent fallback
// then records as an abandonment cutoff before the renomination's own entry is
// recorded, landing it at or below that cutoff. Construct that end state directly
// and check the response is still accepted.
func TestControllingSelector_RenominationAcceptedDespiteAbandonmentCutoffRace(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)
	agent.enableRenomination = true

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateFailed
	pair.nominationAbandonedSeq = 5 // an earlier abandonment's cutoff

	// Recorded with a seq at exactly the cutoff, as the race above would produce.
	renominationReq := bindingRequest{
		timestamp:      time.Now(),
		transactionID:  freshTransactionIDFor(t),
		destination:    remote.addrPort(),
		networkType:    remote.NetworkType(),
		isUseCandidate: true,
		// isPrimaryNomination left false, as a renomination request would have it.
		seq: pair.nominationAbandonedSeq,
	}
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, renominationReq)

	resp := buildSuccessResponse(t, agent, renominationReq.transactionID)
	selector.HandleSuccessResponse(resp, local, remote, remote.addrPort())

	require.Equal(t, CandidatePairStateSucceeded, pair.state,
		"a renomination response must not be discarded by the abandonment cutoff race, even at exactly the cutoff seq")
}

// A goroutine claims seqs concurrently with normal on-loop sendBindingRequest
// calls, without touching pendingBindingRequests. Every seq must be unique, and a
// seq claimed afterward must be strictly greater than any of them.
func TestAgent_BindingRequestSeqIsSafeForConcurrentOffLoopClaims(t *testing.T) {
	agent, selector := newFallbackTestAgent(t)

	local := fallbackTestCand("10.24.0.236", 60170)
	remote := fallbackTestCand("10.24.0.30", 61883)
	pair := agent.addPair(local, remote)
	pair.state = CandidatePairStateSucceeded

	const concurrentClaims = 200
	claimed := make(chan uint64, concurrentClaims)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range concurrentClaims {
			claimed <- agent.bindingRequestSeq.Add(1)
		}
	}()

	// On-loop activity claims its own seq concurrently with the goroutine above.
	selector.PingCandidate(local, remote)
	onLoopReq := lastPendingBindingRequest(t, agent)

	wg.Wait()
	close(claimed)

	seen := make(map[uint64]bool, concurrentClaims+1)
	seen[onLoopReq.seq] = true
	for seq := range claimed {
		require.NotZero(t, seq)
		require.False(t, seen[seq], "seq %d claimed more than once", seq)
		seen[seq] = true
	}
	require.Len(t, seen, concurrentClaims+1, "every claimed seq, on-loop and off-loop alike, must be unique")

	// A seq claimed afterward must exceed a cutoff read at this point.
	cutoff := agent.bindingRequestSeq.Load()
	selector.PingCandidate(local, remote)
	laterReq := lastPendingBindingRequest(t, agent)
	require.Greater(t, laterReq.seq, cutoff)
}
