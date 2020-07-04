package ice

import (
	"net"
	"strconv"
	"sync"

	"github.com/pion/logging"
)

// tcpMuxes is a map of local addr listeners to tcpMux
var tcpMuxes map[string]*tcpMux
var tcpMuxesMu sync.Mutex

type tcpIPMux struct {
	params *tcpIPMuxParams
	wg     sync.WaitGroup
}

type tcpIPMuxParams struct {
	ListenPort     int
	ReadBufferSize int
	Logger         logging.LeveledLogger
}

func newTCPIPMux(params tcpIPMuxParams) *tcpIPMux {
	m := &tcpIPMux{
		params: &params,
	}

	tcpMuxesMu.Lock()

	if tcpMuxes == nil {
		tcpMuxes = map[string]*tcpMux{}
	}

	tcpMuxesMu.Unlock()

	return m
}

func (m *tcpIPMux) Remove(key string) {
	tcpMuxesMu.Lock()
	defer tcpMuxesMu.Unlock()

	if tcpMux, ok := tcpMuxes[key]; ok {
		err := tcpMux.Close()
		if err != nil {
			m.params.Logger.Errorf("Error closing tcpMux for key: %s: %s", key, err)
		}
		delete(tcpMuxes, key)
	}
}

func (m *tcpIPMux) RemoveUfrag(ufrag string) {
	tcpMuxesMu.Lock()
	defer tcpMuxesMu.Unlock()

	for _, tcpMux := range tcpMuxes {
		tcpMux.RemoveConn(ufrag)
	}
}

func (m *tcpIPMux) Listen(ip net.IP) (*tcpMux, error) {
	tcpMuxesMu.Lock()
	defer tcpMuxesMu.Unlock()

	key := net.JoinHostPort(ip.String(), strconv.Itoa(m.params.ListenPort))

	tcpMux, ok := tcpMuxes[key]
	if ok {
		return tcpMux, nil
	}

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   ip,
		Port: m.params.ListenPort,
	})

	if err != nil {
		return nil, err
	}

	key = net.JoinHostPort(ip.String(), strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))

	tcpMux = newTCPMux(tcpMuxParams{
		Listener:       listener,
		Logger:         m.params.Logger,
		ReadBufferSize: m.params.ReadBufferSize,
	})

	tcpMuxes[key] = tcpMux

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		<-tcpMux.CloseChannel()
		m.Remove(key)
	}()

	return tcpMux, nil
}
