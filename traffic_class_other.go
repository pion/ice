// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !unix

package ice

import "net"

func setPacketConnTrafficClass(net.PacketConn, int) error {
	return nil
}
