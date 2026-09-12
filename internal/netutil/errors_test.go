// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package netutil

import (
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNetworkErrorReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"family", errAddressFamilyUnsupported, "the local system does not support the requested address family"},
		{"address", syscall.EADDRNOTAVAIL, "the requested local address is unavailable"},
		{"network", errNetworkUnreachable, "there is no route to the destination network"},
		{"host", errHostUnreachable, "the destination host is unreachable"},
		{"timeout", os.ErrDeadlineExceeded, "the operation timed out; check connectivity and server availability"},
		{"unknown", io.ErrUnexpectedEOF, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("gather: %w", &net.OpError{
				Op: "write", Net: "udp6", Err: &os.SyscallError{Syscall: "sendto", Err: tc.err},
			})
			assert.Equal(t, tc.want, NetworkErrorReason(err))
		})
	}
}
