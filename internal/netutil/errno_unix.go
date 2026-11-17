// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !windows

// Package netutil provides network-related helpers.
package netutil

import (
	"errors"
	"syscall"
)

// IsAddrUnavailable reports whether the local address is unavailable or invalid
// for the requested listen operation, regardless of the port.
func IsAddrUnavailable(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EADDRNOTAVAIL || errno == syscall.EINVAL
	}

	return false
}
