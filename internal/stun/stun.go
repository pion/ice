// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package stun contains ICE specific STUN code
package stun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/stun/v4"
)

// RFC 5389 Section 7.2.1 defines RTO, Rc, and Rm:
// https://www.rfc-editor.org/rfc/rfc5389#section-7.2.1
const (
	getXORMappedAddrInitialRTO          = 500 * time.Millisecond // RTO
	getXORMappedAddrMaxRequests         = 7                      // Rc
	getXORMappedAddrFinalWaitMultiplier = 16                     // Rm
)

var (
	errGetXorMappedAddrResponse = errors.New("failed to get XOR-MAPPED-ADDRESS response")
	errMismatchUsername         = errors.New("username mismatch")
)

// RunPacketConn runs the transaction on a dedicated packet connection. The
// transaction reads responses from conn while Get owns retransmission timing.
func (t *XORMappedAddrTransaction) RunPacketConn( //nolint:cyclop
	ctx context.Context,
	conn net.PacketConn,
	serverAddr net.Addr,
	timeout time.Duration,
) (*stun.XORMappedAddress, error) {
	if timeout == 0 {
		timeout = time.Duration(1<<63 - 1)
	}
	deadline := time.Now().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	defer conn.SetDeadline(time.Time{}) //nolint:errcheck

	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-stop:
		}
	}()

	var readerDone chan struct{}
	addr, err := t.Get(ctx, time.Until(deadline), func(_ context.Context, request []byte) error {
		if _, writeErr := conn.WriteTo(request, serverAddr); writeErr != nil {
			return writeErr
		}

		if readerDone == nil {
			readerDone = make(chan struct{})
			go t.readPacketConn(conn, serverAddr, readerDone)
		}

		return nil
	})
	close(stop)
	<-stopped
	if readerDone != nil {
		select {
		case <-readerDone:
		default:
			if deadlineErr := conn.SetDeadline(time.Now()); deadlineErr == nil {
				<-readerDone
			}
		}
	}
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return addr, err
}

func (t *XORMappedAddrTransaction) readPacketConn(conn net.PacketConn, serverAddr net.Addr, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 1280)
	for {
		n, sourceAddr, err := conn.ReadFrom(buf)
		if err != nil {
			t.complete(nil, err)

			return
		}

		response := &stun.Message{Raw: buf[:n]}
		if err = response.Decode(); err != nil {
			continue
		}

		if sameAddr(sourceAddr, serverAddr) && t.HandleResponse(response) {
			return
		}
	}
}

func sameAddr(a, b net.Addr) bool {
	aUDP, aOK := a.(*net.UDPAddr)
	bUDP, bOK := b.(*net.UDPAddr)
	if !aOK || !bOK {
		return false
	}
	aAddr, bAddr := aUDP.AddrPort(), bUDP.AddrPort()

	return aAddr.Port() == bAddr.Port() && aAddr.Addr().Unmap() == bAddr.Addr().Unmap()
}

// XORMappedAddrTransaction is a stateful STUN binding transaction. Responses
// may be delivered asynchronously with HandleResponse.
type XORMappedAddrTransaction struct {
	request       *stun.Message
	done          chan struct{}
	completeOnce  sync.Once
	mappedAddress *stun.XORMappedAddress
	err           error
	completedAt   time.Time
}

// NewXORMappedAddrTransaction creates a STUN binding transaction.
func NewXORMappedAddrTransaction() (*XORMappedAddrTransaction, error) {
	request, err := stun.Build(stun.BindingRequest, stun.TransactionID)
	if err != nil {
		return nil, err
	}

	return &XORMappedAddrTransaction{
		request: request,
		done:    make(chan struct{}),
	}, nil
}

// ID returns the transaction ID used to route STUN responses.
func (t *XORMappedAddrTransaction) ID() [stun.TransactionIDSize]byte {
	return t.request.TransactionID
}

// Get runs the transaction and waits for its result. A non-positive timeout
// expires before sending.
func (t *XORMappedAddrTransaction) Get( //nolint:cyclop
	ctx context.Context,
	timeout time.Duration,
	send func(context.Context, []byte) error,
) (*stun.XORMappedAddress, error) {
	deadline := time.Now().Add(timeout)
	transactionCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for attempt := 0; ; attempt++ {
		select {
		case <-t.done:
			return t.mappedAddress, t.err
		default:
		}
		if transactionCtx.Err() != nil {
			return nil, transactionError(ctx)
		}

		if err := send(transactionCtx, t.request.Raw); err != nil {
			if transactionCtx.Err() != nil {
				return nil, transactionError(ctx)
			}

			return nil, err
		}
		wait := getXORMappedAddrInitialRTO << attempt
		if attempt == getXORMappedAddrMaxRequests-1 {
			wait = getXORMappedAddrInitialRTO * getXORMappedAddrFinalWaitMultiplier
		}
		attemptDeadline := time.Now().Add(wait)
		if attemptDeadline.After(deadline) {
			attemptDeadline = deadline
		}
		timer := time.NewTimer(time.Until(attemptDeadline))
		select {
		case <-t.done:
			timer.Stop()

			return t.mappedAddress, t.err
		case <-timer.C:
		case <-transactionCtx.Done():
			timer.Stop()

			return nil, transactionError(ctx)
		}
		if attempt == getXORMappedAddrMaxRequests-1 {
			return nil, os.ErrDeadlineExceeded
		}
	}
}

func transactionError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return os.ErrDeadlineExceeded
}

// Cached returns a successful result while it remains within ttl.
func (t *XORMappedAddrTransaction) Cached(ttl time.Duration) (*stun.XORMappedAddress, bool) {
	select {
	case <-t.done:
	default:
		return nil, false
	}
	if t.mappedAddress == nil || t.err != nil || t.completedAt.Add(ttl).Before(time.Now()) {
		return nil, false
	}

	return t.mappedAddress, true
}

// HandleResponse validates and completes the transaction with a decoded STUN
// response. It reports whether the message belongs to this transaction.
func (t *XORMappedAddrTransaction) HandleResponse(msg *stun.Message) bool {
	if msg.TransactionID != t.ID() ||
		msg.Type.Method != stun.MethodBinding ||
		(msg.Type.Class != stun.ClassSuccessResponse && msg.Type.Class != stun.ClassErrorResponse) {
		return false
	}
	if msg.Type.Class == stun.ClassErrorResponse {
		var code stun.ErrorCodeAttribute
		err := code.GetFrom(msg)
		if err == nil {
			code.Reason = bytes.Clone(code.Reason)
			err = stun.TurnError{StunMessageType: msg.Type, ErrorCodeAttr: code}
		}
		t.complete(nil, err)

		return true
	}

	var addr stun.XORMappedAddress
	err := addr.GetFrom(msg)
	if errors.Is(err, stun.ErrAttributeNotFound) {
		var mappedAddr stun.MappedAddress
		if err = mappedAddr.GetFrom(msg); err == nil {
			addr.IP, addr.Port = mappedAddr.IP, mappedAddr.Port
		}
	}
	if err != nil {
		t.complete(nil, fmt.Errorf("%w: %v", errGetXorMappedAddrResponse, err)) //nolint:errorlint

		return true
	}

	t.complete(&addr, nil)

	return true
}

func (t *XORMappedAddrTransaction) complete(addr *stun.XORMappedAddress, err error) {
	t.completeOnce.Do(func() {
		t.mappedAddress = addr
		t.err = err
		t.completedAt = time.Now()
		close(t.done)
	})
}

// AssertUsername checks that the given STUN message m has a USERNAME attribute with a given value.
func AssertUsername(m *stun.Message, expectedUsername string) error {
	var username stun.Username
	if err := username.GetFrom(m); err != nil {
		return err
	} else if string(username) != expectedUsername {
		return fmt.Errorf("%w expected(%x) actual(%x)", errMismatchUsername, expectedUsername, string(username))
	}

	return nil
}
