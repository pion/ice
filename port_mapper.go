// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import "fmt"

type portMapper struct {
	udpMap map[int]int
	tcpMap map[int]int
}

func newPortMapper(mappings []PortMapping) (*portMapper, error) {
	pm := &portMapper{
		udpMap: make(map[int]int),
		tcpMap: make(map[int]int),
	}

	for _, mapping := range mappings {
		if err := pm.mapPort(mapping.InternalPort, mapping.ExternalPort, mapping.Protocol); err != nil {
			return nil, err
		}
	}

	return pm, nil
}

// Add the port mapping to port mapper. Adding same internal port will override previous mapping.
// Not enforcing that each internal port is mapped to a unique external port.
func (pm *portMapper) mapPort(internalPort int, externalPort int, protocol string) error {
	switch protocol {
	case "udp":
		pm.udpMap[internalPort] = externalPort

		return nil
	case "", "tcp":
		pm.tcpMap[internalPort] = externalPort

		return nil
	}

	return fmt.Errorf("unsupported protocol (supported: udp, tcp)")
}

func (pm *portMapper) getExternalPort(internalPort int, protocol string) int {
	switch protocol {
	case "udp":
		if externalPort, exists := pm.udpMap[internalPort]; exists {
			return externalPort
		}
	case "tcp":
		if externalPort, exists := pm.tcpMap[internalPort]; exists {
			return externalPort
		}
	}

	return internalPort
}
