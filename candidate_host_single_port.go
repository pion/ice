package ice

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

var (
	candidateMap map[string](*UdpMuxedPacketConn)
	udpMuxer     *net.UDPConn
	writerChan   chan muxedWriteRequest

	enableRaaderLogs = false
	enableWriterLogs = false
)

// CandidateHost is a candidate of type host
type CandidateHostMuxed struct {
	candidateBase

	network string
}

// CandidateHostConfig is the config required to create a new CandidateHost
type CandidateHostMuxedConfig struct {
	CandidateID string
	Network     string
	Address     string
	Port        int
	Component   uint16
	Priority    uint32
	Foundation  string
	TCPType     TCPType
}

type muxedReadResponse struct {
	size int
	addr net.Addr
	data []byte
}

type muxedWriteRequest struct {
	p         []byte
	addr      net.Addr
	writeDone chan muxedWriteCompleted
}

type muxedWriteCompleted struct {
	n   int
	err error
}

type UdpMuxedPacketConn struct {
	remoteIdetifier []string
	remoteSetupDone chan string
	readChan        chan muxedReadResponse
	writeDone       chan muxedWriteCompleted
}

func (m *UdpMuxedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(m.remoteIdetifier) == 0 {
		log.Println("[MUX_CONN][R] remote identifier not yet set. Waiting for it")

		<-m.remoteSetupDone
		log.Println("[MUX_CONN][R] Remote identifier setup complete: ", m.remoteIdetifier)
	}

	logReader("[MUX_CONN][R] Waiting for readFrom....")

	response := <- m.readChan
	copy(p, response.data)
	logReader("[MUX_CONN][R] Received data from connection service: ", response.addr.String(), response.size)
	return response.size, response.addr, nil
}

func (m *UdpMuxedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {

	logWriter("[MUX_CONN][W] Write request to add: ", addr.String())
	if len(m.remoteIdetifier) == 0 {
		log.Println("[MUX_CONN][W] Setting remote identifier: ", addr.String())
		m.remoteIdetifier = append(m.remoteIdetifier, addr.String())
		m.remoteSetupDone <- addr.String()
		candidateMap[addr.String()] = m
	} else if !contains(m.remoteIdetifier, addr.String()) {
		log.Printf("[MUX_CONN][W][ERROR] Remote identifer getting changed. Previous: %s, Now: %s \n ", m.remoteIdetifier, addr.String())
		m.remoteIdetifier = append(m.remoteIdetifier, addr.String())
		candidateMap[addr.String()] = m
	} else {
		logWriter("[MUX_CONN][W] Reseinding on same old remote: ", addr.String())
	}

	return udpMuxer.WriteTo(p, addr)

	//
	//logWriter("[MUX_CONN][W] Sending write request")
	//writerChan <- muxedWriteRequest{
	//	p:    p,
	//	addr: addr,
	//	writeDone: m.writeDone,
	//}
	//
	//logWriter("[MUX_CONN][W] Waiting for Write Complete : ")
	//writeComplete := <-m.writeDone
	//logWriter("[MUX_CONN][W] Write Complete : ", writeComplete)
	//return writeComplete.n, writeComplete.err
}

func (m *UdpMuxedPacketConn) Close() error {
	log.Println("[MUX_CONN] Close() ", m.remoteIdetifier)
	if len(m.remoteIdetifier) == 0 {
		return nil
	}

	for _, iden := range m.remoteIdetifier {
		delete(candidateMap, iden)
	}
	return nil
}

func (m *UdpMuxedPacketConn) LocalAddr() net.Addr {
	return udpMuxer.LocalAddr()
}

func (m *UdpMuxedPacketConn) SetDeadline(t time.Time) error {
	log.Println("[SetDeadline] ", t)
	return nil
}

func (m *UdpMuxedPacketConn) SetReadDeadline(t time.Time) error {
	log.Println("[SetReadDeadline] ", t)
	return nil
}

func (m *UdpMuxedPacketConn) SetWriteDeadline(t time.Time) error {
	log.Println("[SetWriteDeadline] ", t)
	return nil
}

func NewUdpMuxedConnection() (*UdpMuxedPacketConn, error) {
	conn := UdpMuxedPacketConn{
		remoteIdetifier: []string{},
		remoteSetupDone: make(chan string, 1024),
		readChan:        make(chan muxedReadResponse, 1024 * 10),
		writeDone:       make(chan muxedWriteCompleted, 1024 * 10),
	}

	return &conn, nil
}

// NewCandidateHost creates a new host candidate
func NewCandidateHostMuxed(config *CandidateHostMuxedConfig) (*CandidateHostMuxed, error) {
	candidateID := config.CandidateID

	if candidateID == "" {
		candidateID = globalCandidateIDGenerator.Generate()
	}

	c := &CandidateHostMuxed{
		candidateBase: candidateBase{
			id:                 candidateID,
			address:            config.Address,
			candidateType:      CandidateTypeHost,
			component:          config.Component,
			port:               config.Port,
			tcpType:            config.TCPType,
			foundationOverride: config.Foundation,
			priorityOverride:   config.Priority,
		},
		network: config.Network,
	}

	if !strings.HasSuffix(config.Address, ".local") {
		ip := net.ParseIP(config.Address)
		if ip == nil {
			return nil, ErrAddressParseFailed
		}

		if err := c.setIP(ip); err != nil {
			return nil, err
		}
	} else {
		// Until mDNS candidate is resolved assume it is UDPv4
		c.candidateBase.networkType = NetworkTypeUDP4
	}

	return c, nil
}

func (c *CandidateHostMuxed) setIP(ip net.IP) error {
	networkType, err := determineNetworkType(c.network, ip)
	if err != nil {
		return err
	}

	c.candidateBase.networkType = networkType
	c.candidateBase.resolvedAddr = createAddr(networkType, ip, c.port)

	return nil
}

func StartUdpMuxerService(port int) {
	context, _ := context.WithCancel(context.Background())
	go startUpdMux(context, &net.UDPAddr{
		//IP:   net.ParseIP("192.168.1.25"),
		Port: port,
	})

}

// server wraps all the UDP echo server functionality.
// ps.: the server is capable of answering to a single
// client at a time.
func startUpdMux(ctx context.Context, locAddr *net.UDPAddr) (err error) {
	// Initialize channels
	writerChan = make(chan muxedWriteRequest, 1024*1024)
	candidateMap = map[string]*UdpMuxedPacketConn{}

	// ListenPacket provides us a wrapper around ListenUDP so that
	// we don't need to call `net.ResolveUDPAddr` and then subsequentially
	// perform a `ListenUDP` with the UDP address.
	//
	// The returned value (PacketConn) is pretty much the same as the one
	// from ListenUDP (UDPConn) - the only difference is that `Packet*`
	// methods and interfaces are more broad, also covering `ip`.
	udpMuxer, err = net.ListenUDP("udp", locAddr)
	if err != nil {
		log.Println("[ERROR] Unable to start udp muxer")
		return
	}

	log.Println("Started udp muxer on port: ", locAddr.Port)
	// `Close`ing the packet "connection" means cleaning the data structures
	// allocated for holding information about the listening socket.
	defer udpMuxer.Close()

	doneChan := make(chan error, 1)
	go startReader(doneChan, udpMuxer)
	//go startWriter(udpMuxer)

	// Wait for finish
	select {
	case <-ctx.Done():
		fmt.Println("cancelled")
		err = ctx.Err()
	case err = <-doneChan:
	}

	return
}

func startWriter(conn *net.UDPConn) {
	for {
		logWriter("[WRITER] Waiting for write requests.....")
		writeRequest := <-writerChan

		// TODO - Remove this check
		_, ok := candidateMap[writeRequest.addr.String()]
		if !ok {
			log.Println("[WRITER][ERROR] Cound not find candidate with wrror")
		}

		logWriter("[WRITER] Writing request: ", writeRequest.addr)
		n, err := conn.WriteTo(writeRequest.p, writeRequest.addr)
		if err != nil {
			log.Println("[WRITER][ERROR] Unable to write to addres: ", writeRequest.addr.String())
			writeRequest.writeDone <- muxedWriteCompleted{
				n:   0,
				err: err,
			}
			return
		}

		logWriter("[WRITER] Write request done..")
		writeRequest.writeDone <- muxedWriteCompleted{
			n:   n,
			err: nil,
		}
	}
}

func startReader(doneChan chan error, conn *net.UDPConn) {
	buffer := make([]byte, receiveMTU)
	for {
		logReader("[READER] Waiting for read data...")
		n, addr, err := conn.ReadFrom(buffer)
		if err != nil {
			log.Println("error reading data from socket: ", err)
			continue
		}

		logReader("[READER] Read data in mux service: ", n)
		candidate, ok := candidateMap[addr.String()]
		if !ok {
			log.Println("[READER][ERROR] No candidate found for data from connection: ", addr.String())
			log.Println("[READER][ERROR] Known candidaites: ", candidateMap)
			continue
		}

		candidateBuffer := make([]byte, n)
		copiedN := copy(candidateBuffer, buffer[:n])
		if copiedN < n {
			log.Printf("[WRITER][ERROR] Lesser bytes coped to muxed buffer. Copied: %d, Original: %d", copiedN, n)
		}

		readResponse := muxedReadResponse{
			size: n,
			addr: addr,
			data: candidateBuffer,
		}
		candidate.readChan <- readResponse
	}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func logReader(v ...interface{}) {
	if enableRaaderLogs {
		log.Println(v...)
	}
}

func logWriter(v ...interface{}) {
	if enableWriterLogs {
		log.Println(v...)
	}
}
