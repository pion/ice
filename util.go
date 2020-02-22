package ice

import (
	"fmt"
	"math/rand"
	"net"
	"sync/atomic"
	"time"

	"github.com/pion/stun"
)

type atomicError struct{ v atomic.Value }

func (a *atomicError) Store(err error) {
	a.v.Store(struct{ error }{err})
}
func (a *atomicError) Load() error {
	err, _ := a.v.Load().(struct{ error })
	return err.error
}

// The conditions of invalidation written below are defined in
// https://tools.ietf.org/html/rfc8445#section-5.1.1.1
func isSupportedIPv6(ip net.IP) bool {
	if len(ip) != net.IPv6len ||
		!isZeros(ip[0:12]) || // !(IPv4-compatible IPv6)
		ip[0] == 0xfe && ip[1]&0xc0 == 0xc0 || // !(IPv6 site-local unicast)
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() {
		return false
	}
	return true
}

func isZeros(ip net.IP) bool {
	for i := 0; i < len(ip); i++ {
		if ip[i] != 0 {
			return false
		}
	}
	return true
}

// RandSeq generates a random alpha numeric sequence of the requested length
func randSeq(n int) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	letters := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}

func parseAddr(in net.Addr) (net.IP, int, NetworkType, bool) {
	switch addr := in.(type) {
	case *net.UDPAddr:
		return addr.IP, addr.Port, NetworkTypeUDP4, true
	case *net.TCPAddr:
		return addr.IP, addr.Port, NetworkTypeTCP4, true
	}
	return nil, 0, 0, false
}

func addrEqual(a, b net.Addr) bool {
	aIP, aPort, aType, aOk := parseAddr(a)
	if !aOk {
		return false
	}

	bIP, bPort, bType, bOk := parseAddr(b)
	if !bOk {
		return false
	}

	return aType == bType && aIP.Equal(bIP) && aPort == bPort
}

func generateCandidateID() (string, error) {
	return generateRandString("candidate:", "")
}

func generateRandString(prefix, sufix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.New(rand.NewSource(time.Now().UnixNano())).Read(b); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s%X-%X-%X-%X-%X%s", prefix, b[0:4], b[4:6], b[6:8], b[8:10], b[10:], sufix), nil
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
