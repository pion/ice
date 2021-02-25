package ice

import (
	"context"
	"log"
	"net"
	"sync"
	"time"
)

var (
	udpMuxServiceMap map[int]*UDPMuxService
	serviceLock      sync.Mutex
)

type muxedReadResponse struct {
	size int
	addr net.Addr
	data []byte
}

type UDPMuxService struct {
	port             int
	candidateMap     map[string](*UdpMuxedPacketConn)
	writerChan       chan muxedWriteRequest
	udpMuxer         *net.UDPConn
	enableRaaderLogs bool
	enableWriterLogs bool
	cancle           context.CancelFunc
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
	service         *UDPMuxService
}

func (m *UdpMuxedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(m.remoteIdetifier) == 0 {
		log.Println("[MUX_CONN][R] remote identifier not yet set. Waiting for it")

		<-m.remoteSetupDone
		log.Println("[MUX_CONN][R] Remote identifier setup complete: ", m.remoteIdetifier)
	}

	m.service.logReader("[MUX_CONN][R] Waiting for readFrom....")

	response := <-m.readChan
	copy(p, response.data)
	m.service.logReader("[MUX_CONN][R] Received data from connection service: ", response.addr.String(), response.size)
	return response.size, response.addr, nil
}

func (m *UdpMuxedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	m.service.logWriter("[MUX_CONN][W] Write request to add: ", addr.String())

	if len(m.remoteIdetifier) == 0 {
		log.Println("[MUX_CONN][W] Setting remote identifier: ", addr.String())
		m.remoteIdetifier = append(m.remoteIdetifier, addr.String())
		m.remoteSetupDone <- addr.String()
		m.service.candidateMap[addr.String()] = m
	} else if !contains(m.remoteIdetifier, addr.String()) {
		log.Printf("[MUX_CONN][W][ERROR] Remote identifer getting changed. Previous: %s, Now: %s \n ", m.remoteIdetifier, addr.String())
		m.remoteIdetifier = append(m.remoteIdetifier, addr.String())
		m.service.candidateMap[addr.String()] = m
	}

	return m.service.udpMuxer.WriteTo(p, addr)
}

func (m *UdpMuxedPacketConn) Close() error {
	log.Println("[MUX_CONN] Close() ", m.remoteIdetifier)
	if len(m.remoteIdetifier) == 0 {
		return nil
	}

	for _, iden := range m.remoteIdetifier {
		delete(m.service.candidateMap, iden)
	}
	return nil
}

func (m *UdpMuxedPacketConn) LocalAddr() net.Addr {
	return m.service.udpMuxer.LocalAddr()
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

func NewUdpMuxedConnection(port int) (*UdpMuxedPacketConn, error) {
	service := NewUDPMuxService(port)
	conn := UdpMuxedPacketConn{
		remoteIdetifier: []string{},
		remoteSetupDone: make(chan string, 1024),
		readChan:        make(chan muxedReadResponse, 1024*10),
		writeDone:       make(chan muxedWriteCompleted, 1024*10),
		service:         service,
	}

	return &conn, nil
}

// Starts a new UDP Mux Service is not already present.
// Returns exisiting service if already started
func NewUDPMuxService(port int) *UDPMuxService {
	serviceLock.Lock()
	defer serviceLock.Unlock()

	if udpMuxServiceMap == nil {
		udpMuxServiceMap = make(map[int]*UDPMuxService)
	}

	service, ok := udpMuxServiceMap[port]
	if ok {
		return service
	}

	ctx, cancel := context.WithCancel(context.Background())
	service = &UDPMuxService{
		port:             port,
		candidateMap:     map[string]*UdpMuxedPacketConn{},
		writerChan:       make(chan muxedWriteRequest, 1024*1024),
		enableRaaderLogs: false,
		enableWriterLogs: true,
		cancle:           cancel,
	}

	udpMuxServiceMap[port] = service
	service.startUpdMux(ctx)
	return service
}

func (s *UDPMuxService) startUpdMux(ctx context.Context ) (err error) {
	locAddr := &net.UDPAddr{Port: s.port}
	s.udpMuxer, err = net.ListenUDP("udp", locAddr)
	if err != nil {
		log.Println("[ERROR] Unable to start udp muxer", err)
		return
	}
	log.Println("Started udp muxer on port: ", locAddr.Port)

	doneChan := make(chan error, 1)
	go s.startReader(doneChan, s.udpMuxer)

	// Wait for finish
	go func() {
		defer s.udpMuxer.Close()

		select {
		case <-ctx.Done():
			err = ctx.Err()
		case err = <-doneChan:
		}
	}()

	return
}

func (s *UDPMuxService) startReader(doneChan chan error, conn *net.UDPConn) {
	buffer := make([]byte, receiveMTU)
	for {
		s.logReader("[READER] Waiting for read data...")
		n, addr, err := conn.ReadFrom(buffer)
		if err != nil {
			log.Println("error reading data from socket: ", err)
			continue
		}

		s.logReader("[READER] Read data in mux service: ", n)
		candidate, ok := s.candidateMap[addr.String()]
		if !ok {
			log.Println("[READER][ERROR] No candidate found for data from connection: ", addr.String())
			log.Println("[READER][ERROR] Known candidaites: ", s.candidateMap)
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

func (s *UDPMuxService) logReader(v ...interface{}) {
	if s.enableRaaderLogs {
		log.Println(v...)
	}
}

func (s *UDPMuxService) logWriter(v ...interface{}) {
	if s.enableWriterLogs {
		log.Println(v...)
	}
}
