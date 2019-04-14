package ice

import "net"

func localInterfaces(networkTypes []NetworkType) (ips []net.IP) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}

	var IPv4Requested, IPv6Requested bool
	for _, typ := range networkTypes {
		if typ.IsIPv4() {
			IPv4Requested = true
		}

		if typ.IsIPv6() {
			IPv6Requested = true
		}
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}

		addrs, err := iface.Addrs()
		if err != nil {
			return ips
		}

		for _, addr := range addrs {
			var ip net.IP
			switch addr := addr.(type) {
			case *net.IPNet:
				ip = addr.IP
			case *net.IPAddr:
				ip = addr.IP

			}
			if ip == nil || ip.IsLoopback() {
				continue
			}

			if ipv4 := ip.To4(); ipv4 == nil {
				if !IPv6Requested {
					continue
				} else if !isSupportedIPv6(ip) {
					continue
				}
			} else if !IPv4Requested {
				continue
			}

			ips = append(ips, ip)
		}
	}
	return ips
}

func listenUDP(portMax, portMin int, network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
	if (laddr.Port != 0) || ((portMin == 0) && (portMax == 0)) {
		return net.ListenUDP(network, laddr)
	}
	var i, j int
	i = portMin
	if i == 0 {
		i = 1
	}
	j = portMax
	if j == 0 {
		j = 0xFFFF
	}
	for i <= j {
		c, e := net.ListenUDP(network, &net.UDPAddr{IP: laddr.IP, Port: i})
		if e == nil {
			return c, e
		}
		i++
	}
	return nil, ErrPort
}

func gatherCandidatesLocal(a *Agent, networkTypes []NetworkType) {
	localIPs := localInterfaces(networkTypes)
	for _, ip := range localIPs {
		for _, network := range supportedNetworks {
			conn, err := listenUDP(int(a.portmax), int(a.portmin), network, &net.UDPAddr{IP: ip, Port: 0})
			if err != nil {
				a.log.Warnf("could not listen %s %s\n", network, ip)
				continue
			}

			port := conn.LocalAddr().(*net.UDPAddr).Port
			c, err := NewCandidateHost(network, ip, port, ComponentRTP)
			if err != nil {
				a.log.Warnf("Failed to create host candidate: %s %s %d: %v\n", network, ip, port, err)
				continue
			}

			networkType := c.NetworkType
			set := a.localCandidates[networkType]
			set = append(set, c)
			a.localCandidates[networkType] = set

			c.start(a, conn)
		}
	}
}

func gatherCandidatesReflective(a *Agent, urls []*URL, networkTypes []NetworkType) {
	for _, networkType := range networkTypes {
		network := networkType.String()
		for _, url := range urls {
			switch url.Scheme {
			case SchemeTypeSTUN:
				laddr, xoraddr, err := allocateUDP(network, url)
				if err != nil {
					a.log.Warnf("could not allocate %s %s: %v\n", network, url, err)
					continue
				}
				conn, err := net.ListenUDP(network, laddr)
				if err != nil {
					a.log.Warnf("could not listen %s %s: %v\n", network, laddr, err)
				}

				ip := xoraddr.IP
				port := xoraddr.Port
				relIP := laddr.IP.String()
				relPort := laddr.Port
				c, err := NewCandidateServerReflexive(network, ip, port, ComponentRTP, relIP, relPort)
				if err != nil {
					a.log.Warnf("Failed to create server reflexive candidate: %s %s %d: %v\n", network, ip, port, err)
					continue
				}

				networkType := c.NetworkType
				set := a.localCandidates[networkType]
				set = append(set, c)
				a.localCandidates[networkType] = set

				c.start(a, conn)

			default:
				a.log.Warnf("scheme %s is not implemented\n", url.Scheme)
				continue
			}
		}
	}
}
