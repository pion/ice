// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !windows

// Package netutil provides network-related helpers.
package netutil

import (
	"errors"
	"syscall"
)

const (
	errAddressFamilyUnsupported = syscall.EAFNOSUPPORT
	errNetworkUnreachable       = syscall.ENETUNREACH
	errHostUnreachable          = syscall.EHOSTUNREACH
)

// IsAddrUnavailable reports whether err indicates that the address
// is unavailable (as opposed to a specific port being busy).
func IsAddrUnavailable(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EADDRNOTAVAIL
	}

	return false
}
