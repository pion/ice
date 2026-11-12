// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build unix

package ice

import (
	"errors"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func setPacketConnTrafficClass(conn net.PacketConn, trafficClass int) error {
	var joinedErr error
	if err := ipv4.NewPacketConn(conn).SetTOS(trafficClass); err != nil {
		joinedErr = errors.Join(joinedErr, err)
	} else {
		return nil
	}
	if err := ipv6.NewPacketConn(conn).SetTrafficClass(trafficClass); err != nil {
		joinedErr = errors.Join(joinedErr, err)
	} else {
		return nil
	}

	return joinedErr
}
