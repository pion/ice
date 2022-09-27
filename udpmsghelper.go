//go:build !js && !windows

package ice

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"syscall"

	"github.com/pion/logging"
)

var errUnknownOobData = errors.New("unknown oob data")

func setUDPSocketOptionsForLocalAddr(fd uintptr, logger logging.LeveledLogger) {
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_2292PKTINFO, 1); err != nil {
		logger.Warnf("Failed to set sockopt IPV6_2292PKTINFO: %s", err)
	}
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_PKTINFO, 1); err != nil {
		logger.Warnf("Failed to set sockopt IP_PKTINFO: %s", err)
	}
}

func getLocalAddrFromOob(oob []byte) (net.IP, error) {
	var localHost net.IP
	// get destination local addr from received packet
	oobBuffer := bytes.NewBuffer(oob)
	msg := syscall.Cmsghdr{}
	err := binary.Read(oobBuffer, binary.LittleEndian, &msg)
	if err == nil {
		switch {
		case msg.Level == syscall.IPPROTO_IP && msg.Type == syscall.IP_PKTINFO:
			packetInfo := syscall.Inet4Pktinfo{}
			if err = binary.Read(oobBuffer, binary.LittleEndian, &packetInfo); err == nil {
				localHost = net.IP(packetInfo.Addr[:])
				return localHost, nil
			}
		case msg.Level == syscall.IPPROTO_IPV6 && msg.Type == syscall.IPV6_2292PKTINFO:
			packetInfo := syscall.Inet6Pktinfo{}
			if err = binary.Read(oobBuffer, binary.LittleEndian, &packetInfo); err == nil {
				localHost = net.IP(packetInfo.Addr[:])
				return localHost, nil
			}
		default:
			return localHost, errUnknownOobData
		}
	}
	return localHost, err
}
