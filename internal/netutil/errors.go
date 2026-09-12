// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package netutil

import (
	"errors"
	"net"
)

// NetworkErrorReason describes known network failures without guessing from the
// address family. Unknown errors have no additional explanation.
func NetworkErrorReason(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, errAddressFamilyUnsupported):
		return "the local system does not support the requested address family"
	case IsAddrUnavailable(err):
		return "the requested local address is unavailable"
	case errors.Is(err, errNetworkUnreachable):
		return "there is no route to the destination network"
	case errors.Is(err, errHostUnreachable):
		return "the destination host is unreachable"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "the operation timed out; check connectivity and server availability"
	default:
		return ""
	}
}
