package ice

import (
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync/atomic"
	"time"
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

// flattenErrs flattens multiple errors into one
func flattenErrs(errs []error) error {
	var errstrings []string

	for _, err := range errs {
		if err != nil {
			errstrings = append(errstrings, err.Error())
		}
	}

	if len(errstrings) == 0 {
		return nil
	}

	return fmt.Errorf(strings.Join(errstrings, "\n"))
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
