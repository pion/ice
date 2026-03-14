// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !windows

package ice

import (
	"errors"
	"syscall"
)

// isInterfaceLevelError checks whether a ListenUDP error indicates that
// the address is unavailable (as opposed to a specific port being busy).
// When the address is gone, no port will work, so callers should stop
// iterating immediately.
func isInterfaceLevelError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EADDRNOTAVAIL
	}

	return false
}
