// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package netutil

import (
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAddrUnavailableWinsock(t *testing.T) {
	for _, errno := range []syscall.Errno{wsaeaddrnotavail, wsaeinval} {
		wrapped := &net.OpError{
			Op:  "listen",
			Net: "udp",
			Err: &os.SyscallError{Syscall: "bind", Err: errno},
		}
		require.True(t, IsAddrUnavailable(wrapped))
	}

	require.False(t, IsAddrUnavailable(syscall.Errno(10048)))
}
