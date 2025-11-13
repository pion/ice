// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !linux && !darwin && !freebsd

package ice

import "net"

func newECNPacketReader(net.PacketConn) (packetReader, error) {
	return nil, nil //nolint:nilnil // ECN is unavailable; use the normal read path.
}
