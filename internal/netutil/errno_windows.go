// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build windows

package netutil

import (
	"errors"
	"syscall"
)

// Go's POSIX-compatible constants differ from raw Winsock errors.
const (
	wsaeaddrnotavail syscall.Errno = 10049
	wsaeinval        syscall.Errno = 10022
)

// IsAddrUnavailable reports whether the local address is unavailable or invalid
// for the requested listen operation, regardless of the port.
func IsAddrUnavailable(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EADDRNOTAVAIL || errno == wsaeaddrnotavail ||
			errno == syscall.EINVAL || errno == wsaeinval
	}

	return false
}
