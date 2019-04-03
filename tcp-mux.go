package ice

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/pions/logging"
	"github.com/pions/stun"
)

// ConnReadPacket read 1 packet from stream
// read packet  bytes https://tools.ietf.org/html/rfc4571#section-2
// 2-byte length header prepends each packet:
//     0                   1                   2                   3
//     0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//     ---------------------------------------------------------------
//    |             LENGTH            |  RTP or RTCP packet ...       |
//     ---------------------------------------------------------------
func ConnReadPacket(c net.Conn, b []byte) (int, error) {
	const headerLen = 2
	var tmplen = make([]byte, headerLen)
	var readed, n int
	var err error
	for readed < headerLen {
		if n, err = c.Read(tmplen[readed:headerLen]); err != nil {
			return 0, err
		}
		readed += n
	}
	pktLen := int(binary.BigEndian.Uint16(tmplen))
	if pktLen > cap(b) {
		return pktLen, io.ErrShortBuffer
	}
	readed = 0
	for readed < pktLen {
		if n, err = c.Read(b[readed:pktLen]); err != nil {
			return 0, err
		}
		readed += n
	}
	return readed, nil
}

// one port for all connections.
// first of all - main key is a binding address:port (root listener)
// Second level key - local session ufrag
//

// muxerTCP
type muxerTCP struct {
	packetConns map[string]*packetTCP
	listener    net.Listener
	sync.Mutex
	log logging.LeveledLogger
}

// listeners are global!
var portsMap = make(map[string]*muxerTCP)
var portsMapLock = &sync.Mutex{}

//getMuxerTCP binds listener to muxer lufrag:rufrag
func (a *Agent) getMuxerTCP(network string, laddr *net.TCPAddr) (*muxerTCP, error) {
	// checks muxing listener already armed
	if laddr.Port != 0 {
		if mux, ok := portsMap[laddr.String()]; ok {
			return mux, nil
		}
	}
	listener, err := net.ListenTCP(network, laddr)
	if err != nil {
		a.log.Error(err.Error())
		return nil, ErrPort
	}
	bindToAddr := listener.Addr().String()
	mux := &muxerTCP{
		listener:    listener,
		packetConns: make(map[string]*packetTCP),
		log:         logging.NewDefaultLoggerFactory().NewLogger("ice"), // detach from agent
	}
	portsMap[bindToAddr] = mux
	go mux.listen()
	return mux, nil
}

// FIXME!!!!
func (mux *muxerTCP) freePacketConn(ufrag string) {
	// delete port ref
	portsMapLock.Lock()
	mux.Lock()
	delete(mux.packetConns, ufrag)
	// if listener []packetConns empty - close it
	if len(mux.packetConns) == 0 {
		addr := mux.listener.Addr().String()
		if err := mux.listener.Close(); err != nil {
			mux.log.Warn(err.Error())
		}
		delete(portsMap, addr)
	}
	portsMapLock.Unlock()
}

//listen - listen connection(s) on port
func (mux *muxerTCP) listen() {
	mux.log.Debugf("listen TCP @%v", mux.listener.Addr().String())
	for {
		c, err := mux.listener.Accept()
		if err != nil {
			mux.log.Errorf("listener on %v: %v", mux.listener.Addr().String(), err)
			return
		}
		go mux.bridgeWorker(c)
	}
}

//bridgeWorker conn->packet
func (mux *muxerTCP) bridgeWorker(c net.Conn) {
	defer func() {
		if err := c.Close(); err != nil {
			mux.log.Warn(err.Error())
		}
	}()
	mux.log.Warnf("bridge packetConn for conn %v", c.RemoteAddr().String())
	b := make([]byte, receiveMTU)
	// read & parce first packet from Conn
	n, err := ConnReadPacket(c, b)
	if err != nil {
		mux.log.Warnf("Error @%v : %v", c.RemoteAddr().String(), err)
		return
	}
	b = b[0:n]
	tpc, ufrag := mux.findPacketConn(b)
	if tpc == nil {
		mux.log.Warnf("no packetConn for conn %v ufrag:\"%v\"", c.RemoteAddr().String(), ufrag)
		return
	}
	if tpc.linked {
		mux.log.Warnf("already bind %v ufrag:\"%v\"", c.RemoteAddr().String(), ufrag)
		return
	}
	tpc.linked = true
	srcAddr := c.RemoteAddr()
	tpc.downstream <- &incomingPacket{buffer: b, srcAddr: &srcAddr}
	// tcp->packet
	go func() {
		for {
			b := make([]byte, receiveMTU)
			if n, err = ConnReadPacket(c, b); err == nil {
				b = b[0:n]
				select {
				case tpc.downstream <- &incomingPacket{buffer: b, srcAddr: &srcAddr}:
				default:
				}
				continue
			}
			if err != io.EOF && !strings.Contains(err.Error(), "use of closed network connection") {
				mux.log.Errorf(" %v :\"%v\"", c.RemoteAddr().String(), err.Error())
			}
			break
		}
	}()
	// packet->tcp
	var toConn *[]byte
	for {
		if toConn = <-tpc.upstream; toConn == nil { // slave closed
			mux.freePacketConn(ufrag) // free muxer ufrag ref
			break
		}
		if _, err := c.Write(*toConn); err != nil {
			mux.log.Errorf("%v :\"%v\"", c.RemoteAddr().String(), err.Error())
		}
	}
	tpc.linked = false
}

//findPacketConn returns the corresponding port by the value of the attribute stun.AttrUsername
func (mux *muxerTCP) findPacketConn(b []byte) (socket *packetTCP, username string) {
	m, _ := stun.NewMessage(b)
	if m == nil || m.Method != stun.MethodBinding { // not a stun
		return nil, ""
	}
	// ICE-TCP-PASSIVE is always server (offerer), but Agent can't
	attr, _ := m.GetOneAttribute(stun.AttrIceControlled)
	if attr == nil {
		return nil, ""
	}

	attr, _ = m.GetOneAttribute(stun.AttrUsername)
	if attr == nil {
		return nil, ""
	}
	username = string(attr.Value)

	if socket = mux.packetConns[strings.Split(username, ":")[0]]; socket != nil {
		mux.log.Infof("got socket for %v", username)
	}
	return socket, username
}

// listenTCP works similar to listenUDP
func (a *Agent) listenTCP(network string, laddr *net.TCPAddr) (*packetTCP, error) {
	portsMapLock.Lock()
	defer portsMapLock.Unlock()

	laddr.Port = int(a.tcpport)
	mux, err := a.getMuxerTCP(network, laddr)
	if err != nil {
		return nil, err
	}

	mux.Lock()
	defer mux.Unlock()
	if _, ok := mux.packetConns[a.localUfrag]; ok {
		return nil, fmt.Errorf("duplicate ufrag %v", a.localUfrag)
	}

	conn := newPacketTCP(mux.listener.Addr())
	mux.packetConns[a.localUfrag] = conn
	return conn, nil
}
