package ice

import (
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/vnet"
	"github.com/pion/turn/v2"
)

const (
	stunGatherTimeout = time.Second * 5
)

func (a *Agent) localInterfaces(networkTypes []NetworkType) ([]net.IP, error) {
	ips := []net.IP{}
	ifaces, err := a.net.Interfaces()
	if err != nil {
		return ips, err
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

		if a.interfaceFilter != nil && !a.interfaceFilter(iface.Name) {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
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
	return ips, nil
}

func (a *Agent) listenUDP(portMax, portMin int, network string, laddr *net.UDPAddr) (vnet.UDPPacketConn, error) {
	if (laddr.Port != 0) || ((portMin == 0) && (portMax == 0)) {
		return a.net.ListenUDP(network, laddr)
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
	if i > j {
		return nil, ErrPort
	}

	portStart := rand.Intn(j-i+1) + i
	portCurrent := portStart
	for {
		laddr = &net.UDPAddr{IP: laddr.IP, Port: portCurrent}
		c, e := a.net.ListenUDP(network, laddr)
		if e == nil {
			return c, e
		}
		a.log.Debugf("failed to listen %s: %v", laddr.String(), e)
		portCurrent++
		if portCurrent > j {
			portCurrent = i
		}
		if portCurrent == portStart {
			break
		}
	}
	return nil, ErrPort
}

// GatherCandidates initiates the trickle based gathering process.
func (a *Agent) GatherCandidates() error {
	gatherErrChan := make(chan error, 1)

	runErr := a.run(func(agent *Agent) {
		if a.gatheringState != GatheringStateNew {
			gatherErrChan <- ErrMultipleGatherAttempted
			return
		} else if a.onCandidateHdlr == nil {
			gatherErrChan <- ErrNoOnCandidateHandler
			return
		}

		go a.gatherCandidates()

		gatherErrChan <- nil
	})
	if runErr != nil {
		return runErr
	}
	return <-gatherErrChan
}

func (a *Agent) gatherCandidates() {
	gatherStateUpdated := make(chan bool)
	if err := a.run(func(agent *Agent) {
		a.gatheringState = GatheringStateGathering
		close(gatherStateUpdated)
	}); err != nil {
		a.log.Warnf("failed to set gatheringState to GatheringStateGathering for gatherCandidates: %v", err)
		return
	}
	<-gatherStateUpdated

	for _, t := range a.candidateTypes {
		switch t {
		case CandidateTypeHost:
			a.gatherCandidatesLocal(a.networkTypes)
		case CandidateTypeServerReflexive:
			a.gatherCandidatesSrflx(a.urls, a.networkTypes)
		case CandidateTypeRelay:
			if err := a.gatherCandidatesRelay(a.urls); err != nil {
				a.log.Errorf("Failed to gather relay candidates: %v\n", err)
			}
		}
	}

	if err := a.run(func(agent *Agent) {
		if a.onCandidateHdlr != nil {
			go a.onCandidateHdlr(nil)
		}
	}); err != nil {
		a.log.Warnf("Failed to run onCandidateHdlr task: %v\n", err)
		return
	}

	if err := a.run(func(agent *Agent) {
		a.gatheringState = GatheringStateComplete
	}); err != nil {
		a.log.Warnf("Failed to update gatheringState: %v\n", err)
		return
	}
}

func (a *Agent) gatherCandidatesLocal(networkTypes []NetworkType) {
	var wg sync.WaitGroup
	defer wg.Wait()

	localIPs, err := a.localInterfaces(networkTypes)
	if err != nil {
		a.log.Warnf("failed to iterate local interfaces, host candidates will not be gathered %s", err)
		return
	}

	wg.Add(len(localIPs) * len(supportedNetworks))
	for _, ip := range localIPs {
		mappedIP := ip
		if a.mDNSMode != MulticastDNSModeQueryAndGather && a.extIPMapper != nil && a.extIPMapper.candidateType == CandidateTypeHost {
			if _mappedIP, err := a.extIPMapper.findExternalIP(ip.String()); err == nil {
				mappedIP = _mappedIP
			} else {
				a.log.Warnf("1:1 NAT mapping is enabled but no external IP is found for %s\n", ip.String())
			}
		}

		for _, network := range supportedNetworks {
			go func(network string, ip, mappedIP net.IP) {
				defer wg.Done()
				conn, err := a.listenUDP(int(a.portmax), int(a.portmin), network, &net.UDPAddr{IP: ip, Port: 0})
				if err != nil {
					a.log.Warnf("could not listen %s %s\n", network, ip)
					return
				}

				address := mappedIP.String()
				if a.mDNSMode == MulticastDNSModeQueryAndGather {
					address = a.mDNSName
				}

				port := conn.LocalAddr().(*net.UDPAddr).Port

				hostConfig := CandidateHostConfig{
					Network:   network,
					Address:   address,
					Port:      port,
					Component: ComponentRTP,
				}

				c, err := NewCandidateHost(&hostConfig)
				if err != nil {
					a.log.Warnf("Failed to create host candidate: %s %s %d: %v\n", network, mappedIP, port, err)
					return
				}

				if a.mDNSMode == MulticastDNSModeQueryAndGather {
					if err = c.setIP(ip); err != nil {
						a.log.Warnf("Failed to create host candidate: %s %s %d: %v\n", network, mappedIP, port, err)
						return
					}
				}

				if err := a.run(func(agent *Agent) {
					c.start(a, conn)
					a.addCandidate(c)

					if a.onCandidateHdlr != nil {
						go a.onCandidateHdlr(c)
					}
				}); err != nil {
					a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v\n", err)
				}
			}(network, ip, mappedIP)
		}
	}
}

func (a *Agent) gatherCandidatesSrflx(urls []*URL, networkTypes []NetworkType) {
	var stunURLs []*URL
	for _, url := range urls {
		if url.Scheme == SchemeTypeSTUN {
			stunURLs = append(stunURLs, url)
		}
	}

	for _, networkType := range networkTypes {
		network := networkType.String()
		if len(stunURLs) > 0 {
			for _, url := range stunURLs {
				if url.Scheme != SchemeTypeSTUN {
					continue
				}

				hostPort := fmt.Sprintf("%s:%d", url.Host, url.Port)
				serverAddr, err := a.net.ResolveUDPAddr(network, hostPort)
				if err != nil {
					a.log.Warnf("failed to resolve stun host: %s: %v", hostPort, err)
					continue
				}

				conn, err := a.listenUDP(int(a.portmax), int(a.portmin), network, &net.UDPAddr{IP: nil, Port: 0})
				if err != nil {
					a.log.Warnf("Failed to listen for %s: %v\n", serverAddr.String(), err)
					continue
				}

				xoraddr, err := getXORMappedAddr(conn, serverAddr, stunGatherTimeout)
				if err != nil {
					a.log.Warnf("could not get server reflexive address %s %s: %v\n", network, url, err)
					continue
				}

				laddr := conn.LocalAddr().(*net.UDPAddr)
				ip := xoraddr.IP
				port := xoraddr.Port
				relIP := laddr.IP.String()
				relPort := laddr.Port

				srflxConfig := CandidateServerReflexiveConfig{
					Network:   network,
					Address:   ip.String(),
					Port:      port,
					Component: ComponentRTP,
					RelAddr:   relIP,
					RelPort:   relPort,
				}
				c, err := NewCandidateServerReflexive(&srflxConfig)
				if err != nil {
					a.log.Warnf("Failed to create server reflexive candidate: %s %s %d: %v\n", network, ip, port, err)
					continue
				}

				if err := a.run(func(agent *Agent) {
					c.start(a, conn)
					a.addCandidate(c)

					if a.onCandidateHdlr != nil {
						go a.onCandidateHdlr(c)
					}
				}); err != nil {
					a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v\n", err)
				}
			}
		} else if a.extIPMapper != nil && a.extIPMapper.candidateType == CandidateTypeServerReflexive {
			conn, err := a.listenUDP(int(a.portmax), int(a.portmin), network, &net.UDPAddr{IP: nil, Port: 0})
			if err != nil {
				a.log.Warnf("Failed to listen %s: %v\n", network, err)
				continue
			}

			laddr := conn.LocalAddr().(*net.UDPAddr)
			mappedIP, err := a.extIPMapper.findExternalIP(laddr.IP.String())
			if err != nil {
				a.log.Warnf("1:1 NAT mapping is enabled but no external IP is found for %s\n", laddr.IP.String())
				continue
			}

			srflxConfig := CandidateServerReflexiveConfig{
				Network:   network,
				Address:   mappedIP.String(),
				Port:      laddr.Port,
				Component: ComponentRTP,
				RelAddr:   laddr.IP.String(),
				RelPort:   laddr.Port,
			}
			c, err := NewCandidateServerReflexive(&srflxConfig)
			if err != nil {
				a.log.Warnf("Failed to create server reflexive candidate: %s %s %d: %v\n",
					network,
					mappedIP.String(),
					laddr.Port,
					err)
				continue
			}

			if err := a.run(func(agent *Agent) {
				c.start(a, conn)
				a.addCandidate(c)

				if a.onCandidateHdlr != nil {
					go a.onCandidateHdlr(c)
				}
			}); err != nil {
				a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v\n", err)
			}
		}
	}
}

func (a *Agent) gatherCandidatesRelay(urls []*URL) error {
	network := NetworkTypeUDP4.String() // TODO IPv6
	for _, url := range urls {
		switch {
		case url.Scheme != SchemeTypeTURN:
			continue
		case url.Username == "":
			return ErrUsernameEmpty
		case url.Password == "":
			return ErrPasswordEmpty
		}

		TURNServerAddr := fmt.Sprintf("%s:%d", url.Host, url.Port)
		var (
			locConn net.PacketConn
			err     error
			RelAddr string
			RelPort int
		)

		if url.Proto == ProtoTypeUDP {
			locConn, err = a.net.ListenPacket(network, "0.0.0.0:0")
			if err != nil {
				return err
			}

			RelAddr = locConn.LocalAddr().(*net.UDPAddr).IP.String()
			RelPort = locConn.LocalAddr().(*net.UDPAddr).Port
		} else {
			var (
				tcpAddr *net.TCPAddr
				tcpConn *net.TCPConn
			)

			tcpAddr, err = net.ResolveTCPAddr(NetworkTypeTCP4.String(), TURNServerAddr)
			if err != nil {
				return err
			}

			tcpConn, err = net.DialTCP(NetworkTypeTCP4.String(), nil, tcpAddr)
			if err != nil {
				return err
			}

			RelAddr = tcpConn.LocalAddr().(*net.TCPAddr).IP.String()
			RelPort = tcpConn.LocalAddr().(*net.TCPAddr).Port
			locConn = turn.NewSTUNConn(tcpConn)
		}

		client, err := turn.NewClient(&turn.ClientConfig{
			TURNServerAddr: TURNServerAddr,
			Conn:           locConn,
			Username:       url.Username,
			Password:       url.Password,
			LoggerFactory:  a.loggerFactory,
			Net:            a.net,
		})
		if err != nil {
			return err
		}

		err = client.Listen()
		if err != nil {
			return err
		}

		relayConn, err := client.Allocate()
		if err != nil {
			return err
		}

		raddr := relayConn.LocalAddr().(*net.UDPAddr)

		relayConfig := CandidateRelayConfig{
			Network:   network,
			Component: ComponentRTP,
			Address:   raddr.IP.String(),
			Port:      raddr.Port,
			RelAddr:   RelAddr,
			RelPort:   RelPort,
			OnClose: func() error {
				client.Close()
				return locConn.Close()
			},
		}
		candidate, err := NewCandidateRelay(&relayConfig)
		if err != nil {
			a.log.Warnf("Failed to create relay candidate: %s %s: %v\n",
				network, raddr.String(), err)
			continue
		}

		if err := a.run(func(agent *Agent) {
			candidate.start(a, relayConn)
			a.addCandidate(candidate)

			if a.onCandidateHdlr != nil {
				go a.onCandidateHdlr(candidate)
			}
		}); err != nil {
			a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v\n", err)
		}
	}

	return nil
}

// getXORMappedAddr initiates a stun requests to serverAddr using conn, reads the response and returns
// the XORMappedAddress returned by the stun server.
//
// Adapted from stun v0.2.
func getXORMappedAddr(conn net.PacketConn, serverAddr net.Addr, deadline time.Duration) (*stun.XORMappedAddress, error) {
	if deadline > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(deadline)); err != nil {
			return nil, err
		}
	}
	defer func() {
		if deadline > 0 {
			_ = conn.SetReadDeadline(time.Time{})
		}
	}()
	resp, err := stunRequest(
		func(p []byte) (int, error) {
			n, _, errr := conn.ReadFrom(p)
			return n, errr
		},
		func(b []byte) (int, error) {
			return conn.WriteTo(b, serverAddr)
		},
	)
	if err != nil {
		return nil, err
	}
	var addr stun.XORMappedAddress
	if err = addr.GetFrom(resp); err != nil {
		return nil, fmt.Errorf("failed to get XOR-MAPPED-ADDRESS response: %v", err)
	}
	return &addr, nil
}

func stunRequest(read func([]byte) (int, error), write func([]byte) (int, error)) (*stun.Message, error) {
	req, err := stun.Build(stun.BindingRequest, stun.TransactionID)
	if err != nil {
		return nil, err
	}
	if _, err = write(req.Raw); err != nil {
		return nil, err
	}
	const maxMessageSize = 1280
	bs := make([]byte, maxMessageSize)
	n, err := read(bs)
	if err != nil {
		return nil, err
	}
	res := &stun.Message{Raw: bs[:n]}
	if err := res.Decode(); err != nil {
		return nil, err
	}
	return res, nil
}
