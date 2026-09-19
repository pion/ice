// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"errors"

	"github.com/pion/ice/v4/internal/taskloop"
)

var (
	// ErrPort indicates malformed port is provided.
	ErrPort = errors.New("invalid port")

	// ErrLocalUfragInsufficientBits indicates local username fragment insufficient bits are provided.
	// Have to be at least 4 ice-chars, carrying the 24 bits of randomness RFC 8445 §5.3 requires.
	ErrLocalUfragInsufficientBits = errors.New(
		"local username fragment must be at least 4 ice-chars for 24 bits of randomness",
	)

	// ErrLocalPwdInsufficientBits indicates local password insufficient bits are provided.
	// Have to be at least 22 ice-chars, carrying the 128 bits of randomness RFC 8445 §5.3 requires.
	ErrLocalPwdInsufficientBits = errors.New(
		"local password must be at least 22 ice-chars for 128 bits of randomness",
	)

	// ErrProtoType indicates an unsupported transport type was provided.
	ErrProtoType = errors.New("invalid transport protocol type")

	// ErrClosed indicates the agent is closed.
	ErrClosed = taskloop.ErrClosed

	// ErrNoCandidatePairs indicates agent does not have a valid candidate pair.
	ErrNoCandidatePairs = errors.New("no candidate pairs available")

	// ErrCanceledByCaller indicates agent connection was canceled by the caller.
	ErrCanceledByCaller = errors.New("connecting canceled by caller")

	// ErrMultipleStart indicates agent was started twice.
	ErrMultipleStart = errors.New("attempted to start agent twice")

	// ErrRemoteUfragEmpty indicates agent was started with an empty remote ufrag.
	ErrRemoteUfragEmpty = errors.New("remote ufrag is empty")

	// ErrRemotePwdEmpty indicates agent was started with an empty remote pwd.
	ErrRemotePwdEmpty = errors.New("remote pwd is empty")

	// ErrNoOnCandidateHandler indicates agent was started without OnCandidate.
	ErrNoOnCandidateHandler = errors.New("no OnCandidate provided")

	// ErrInvalidURL indicates a nil STUN/TURN URL.
	ErrInvalidURL = errors.New("invalid nil STUN/TURN URL")

	// ErrUsernameEmpty indicates agent was give TURN URL with an empty Username.
	ErrUsernameEmpty = errors.New("username is empty")

	// ErrPasswordEmpty indicates agent was give TURN URL with an empty Password.
	ErrPasswordEmpty = errors.New("password is empty")

	// ErrAddressParseFailed indicates we were unable to parse a candidate address.
	ErrAddressParseFailed = errors.New("failed to parse address")

	// ErrLiteUsingNonHostCandidates indicates non host candidates were selected for a lite agent.
	ErrLiteUsingNonHostCandidates = errors.New("lite agents must only use host candidates")

	// ErrUselessURLsProvided indicates that one or more URL was provided to the agent but no host
	// candidate required them.
	ErrUselessURLsProvided = errors.New("agent does not need URL with selected candidate types")

	// ErrUnsupportedAddressRewriteCandidateType indicates an unsupported rewrite candidate type.
	ErrUnsupportedAddressRewriteCandidateType = errors.New("unsupported address rewrite candidate type")

	// ErrInvalidAddressRewriteMapping indicates an invalid address rewrite mapping.
	ErrInvalidAddressRewriteMapping = errors.New("invalid address rewrite mapping")

	// ErrMulticastDNSWithAddressRewrite indicates that mDNS gathering conflicts with host address rewriting.
	ErrMulticastDNSWithAddressRewrite = errors.New("mDNS gathering cannot be used with address rewrite for host candidate")

	// ErrIneffectiveAddressRewriteHost indicates that host rewriting was requested with host candidates disabled.
	ErrIneffectiveAddressRewriteHost = errors.New("address rewrite for host candidate ineffective")

	// ErrIneffectiveAddressRewriteSrflx indicates that srflx rewriting was requested with srflx candidates disabled.
	ErrIneffectiveAddressRewriteSrflx = errors.New("address rewrite for srflx candidate ineffective")

	// ErrInvalidMulticastDNSHostName indicates an invalid MulticastDNSHostName.
	ErrInvalidMulticastDNSHostName = errors.New(
		"invalid mDNS HostName, must end with .local and can only contain a single '.'",
	)

	// ErrRunCanceled indicates a run operation was canceled by its individual done.
	ErrRunCanceled = errors.New("run was canceled by done")

	// ErrUnknownCandidateTyp indicates that a candidate had a unknown type value.
	ErrUnknownCandidateTyp = errors.New("unknown candidate typ")

	// ErrDetermineNetworkType indicates that the NetworkType was not able to be parsed.
	ErrDetermineNetworkType = errors.New("unable to determine networkType")

	// ErrOnlyControllingAgentCanRenominate indicates that only controlling agent can renominate.
	ErrOnlyControllingAgentCanRenominate = errors.New("only controlling agent can renominate")

	// ErrRenominationNotEnabled indicates that renomination is not enabled.
	ErrRenominationNotEnabled = errors.New("renomination is not enabled")

	// ErrCandidatePairNotFound indicates that candidate pair was not found.
	ErrCandidatePairNotFound = errors.New("candidate pair not found")

	// ErrCandidatePairNotSucceeded indicates that candidate pair is not in succeeded state.
	ErrCandidatePairNotSucceeded = errors.New("candidate pair not in succeeded state")

	// ErrInvalidNominationAttribute indicates an invalid nomination attribute type was provided.
	ErrInvalidNominationAttribute = errors.New("invalid nomination attribute type")

	// ErrInvalidNominationValueGenerator indicates a nil nomination value generator was provided.
	ErrInvalidNominationValueGenerator = errors.New("nomination value generator cannot be nil")

	// ErrInvalidNetworkMonitorInterval indicates an invalid network monitor interval was provided.
	ErrInvalidNetworkMonitorInterval = errors.New("network monitor interval must be greater than 0")

	errAttributeTooShortICECandidate = errors.New("attribute not long enough to be ICE candidate")
	errCandidatePacketConnNil        = errors.New("candidate packet connection is nil")
	errDuplicateCandidate            = errors.New("candidate already added")
	errClosingConnection             = errors.New("failed to close connection")
	errConnectionAddrAlreadyExist    = errors.New("connection with same remote address already exists")
	errInvalidAddress                = errors.New("invalid address")
	errNoTCPMuxAvailable             = errors.New("no TCP mux is available")
	errNotImplemented                = errors.New("not implemented yet")
	errNoUDPMuxAvailable             = errors.New("no UDP mux is available")
	errParseFoundation               = errors.New("failed to parse foundation")
	errParseComponent                = errors.New("failed to parse component")
	errParsePort                     = errors.New("failed to parse port")
	errParsePriority                 = errors.New("failed to parse priority")
	errParseRelatedAddr              = errors.New("failed to parse related addresses")
	errParseExtension                = errors.New("failed to parse extension")
	errParseTCPType                  = errors.New("failed to parse TCP type")
	errUDPMuxDisabled                = errors.New("UDPMux is not enabled")
	errUnknownRole                   = errors.New("unknown role")
	errWrite                         = errors.New("failed to write")
	errWriteSTUNMessage              = errors.New("failed to send STUN message")
	errWriteSTUNMessageToIceConn     = errors.New("failed to write STUN message to ICE connection")
	errXORMappedAddrTimeout          = errors.New("timeout while waiting for XORMappedAddr")
	errFailedToCastUDPAddr           = errors.New("failed to cast net.Addr to net.UDPAddr")
	errInvalidIPAddress              = errors.New("invalid ip address")
)
