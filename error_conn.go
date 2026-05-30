// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"time"
)

// ErrorConn for net.PacketConn when the value is nil.
type ErrorConn struct{}

func (e *ErrorConn) ReadFrom([]byte) (n int, addr net.Addr, err error) {
	var dummyAddr *net.IPAddr

	return 0, dummyAddr, ErrPacketConnNil
}
func (e *ErrorConn) WriteTo([]byte, net.Addr) (n int, err error) { return 0, ErrPacketConnNil }
func (e *ErrorConn) Close() error                                { return ErrPacketConnNil }
func (e *ErrorConn) LocalAddr() net.Addr {
	var dummyAddr *net.IPAddr

	return dummyAddr
}
func (e *ErrorConn) SetDeadline(time.Time) error      { return ErrPacketConnNil }
func (e *ErrorConn) SetReadDeadline(time.Time) error  { return ErrPacketConnNil }
func (e *ErrorConn) SetWriteDeadline(time.Time) error { return ErrPacketConnNil }
