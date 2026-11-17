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

func TestIsAddrUnavailable(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		errno    syscall.Errno
		expected bool
	}{
		{name: "unavailable address", errno: syscall.EADDRNOTAVAIL, expected: true},
		{name: "invalid address", errno: syscall.EINVAL, expected: true},
		{name: "busy port", errno: syscall.EADDRINUSE},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.expected, IsAddrUnavailable(testCase.errno))
			wrapped := &net.OpError{
				Op:  "listen",
				Net: "udp",
				Err: &os.SyscallError{Syscall: "bind", Err: testCase.errno},
			}
			require.Equal(t, testCase.expected, IsAddrUnavailable(wrapped))
		})
	}

	require.False(t, IsAddrUnavailable(nil))
}
