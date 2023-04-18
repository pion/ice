// Package stun contains ICE specific STUN code
package stun

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pion/stun"
)

var (
	errGetXorMappedAddrResponse = errors.New("failed to get XOR-MAPPED-ADDRESS response")
	errMismatchUsername         = errors.New("username mismatch")
)

// GetXORMappedAddr initiates a stun requests to serverAddr using conn, reads the response and returns
// the XORMappedAddress returned by the STUN server.
func GetXORMappedAddr(conn net.PacketConn, serverAddr net.Addr, timeout time.Duration) (*stun.XORMappedAddress, error) {
	if timeout > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}

		// Reset timeout after completion
		defer conn.SetReadDeadline(time.Time{}) //nolint:errcheck
	}

	req, err := stun.Build(stun.BindingRequest, stun.TransactionID)
	if err != nil {
		return nil, err
	}

	if _, err = conn.WriteTo(req.Raw, serverAddr); err != nil {
		return nil, err
	}

	const maxMessageSize = 1280
	buf := make([]byte, maxMessageSize)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		return nil, err
	}

	res := &stun.Message{Raw: buf[:n]}
	if err = res.Decode(); err != nil {
		return nil, err
	}

	var addr stun.XORMappedAddress
	if err = addr.GetFrom(res); err != nil {
		return nil, fmt.Errorf("%w: %v", errGetXorMappedAddrResponse, err) //nolint:errorlint
	}

	return &addr, nil
}

func GetXORMappedAddrs(conn, conn2 net.PacketConn, serverAddr net.Addr, timeout time.Duration, predictNumber int) ([]*stun.XORMappedAddress, error) {
	if timeout > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}

		// Reset timeout after completion
		defer conn.SetReadDeadline(time.Time{}) //nolint:errcheck

		if err := conn2.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}

		// Reset timeout after completion
		defer conn2.SetReadDeadline(time.Time{}) //nolint:errcheck
	}

	sendStun := func(conn net.PacketConn, addr net.Addr, attr *stun.AttrType, v *[]byte) (*struct {
		xorAddr    *stun.XORMappedAddress
		otherAddr  *stun.OtherAddress
		respOrigin *stun.ResponseOrigin
		mappedAddr *stun.MappedAddress
		software   *stun.Software
	}, error) {
		transId := stun.TransactionID
		fmt.Println("sendStun:", addr, transId)
		req, err := stun.Build(stun.BindingRequest, transId)
		if err != nil {
			fmt.Println("sendStun: Build error: ", err)
			return nil, err
		}
		if attr != nil && v != nil {
			req.Add(*attr, *v)
		}

		if _, err = conn.WriteTo(req.Raw, addr); err != nil {
			fmt.Println("sendStun: WriteTo error: ", err)
			return nil, err
		}

		const maxMessageSize = 1280
		buf := make([]byte, maxMessageSize)
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			fmt.Println("sendStun: ReadFrom error: ", err)
			return nil, err
		}

		res := &stun.Message{Raw: buf[:n]}
		if err = res.Decode(); err != nil {
			fmt.Println("sendStun: Decode error: ", err)
			return nil, err
		}

		r := parse(res)

		return &r, nil
	}

	var addrs []*stun.XORMappedAddress

	ret, err := sendStun(conn, serverAddr, nil, nil)
	if err != nil {
		fmt.Println("sendStun: error: ", err)
		return nil, err
	}

	if ret.xorAddr == nil {
		fmt.Println("sendStun: xorAddr is nil")
		return nil, errGetXorMappedAddrResponse
	}
	addrs = append(addrs, ret.xorAddr)

	if predictNumber == 0 {
		return addrs, nil
	}

	if ret.otherAddr == nil || ret.otherAddr.Port == serverAddr.(*net.UDPAddr).Port {
		return addrs, nil
	}

	newServerAddr := net.UDPAddr{IP: ret.otherAddr.IP, Port: ret.otherAddr.Port}
	ret2, err := sendStun(conn2, &newServerAddr, nil, nil)
	if err != nil {
		fmt.Println("sendStun: error: ", err)
		return nil, err
	}

	if ret2.xorAddr != nil {
		delta := ret2.xorAddr.Port - ret.xorAddr.Port
		if delta < 10 && delta > -10 && delta != 0 {
			addrs = append(addrs, ret2.xorAddr)
			predictPort := ret2.xorAddr.Port
			for i := 0; i < predictNumber; i++ {
				predictPort += delta
				addrs = append(addrs, &stun.XORMappedAddress{IP: ret2.xorAddr.IP, Port: predictPort})
			}
		}
	}

	return addrs, nil
}

func parse(msg *stun.Message) (ret struct {
	xorAddr    *stun.XORMappedAddress
	otherAddr  *stun.OtherAddress
	respOrigin *stun.ResponseOrigin
	mappedAddr *stun.MappedAddress
	software   *stun.Software
},
) {
	ret.mappedAddr = &stun.MappedAddress{}
	ret.xorAddr = &stun.XORMappedAddress{}
	ret.respOrigin = &stun.ResponseOrigin{}
	ret.otherAddr = &stun.OtherAddress{}
	ret.software = &stun.Software{}
	if ret.xorAddr.GetFrom(msg) != nil {
		ret.xorAddr = nil
	}
	if ret.otherAddr.GetFrom(msg) != nil {
		ret.otherAddr = nil
	}
	if ret.respOrigin.GetFrom(msg) != nil {
		ret.respOrigin = nil
	}
	if ret.mappedAddr.GetFrom(msg) != nil {
		ret.mappedAddr = nil
	}
	if ret.software.GetFrom(msg) != nil {
		ret.software = nil
	}
	return ret
}

// AssertUsername checks that the given STUN message m has a USERNAME attribute with a given value
func AssertUsername(m *stun.Message, expectedUsername string) error {
	var username stun.Username
	if err := username.GetFrom(m); err != nil {
		return err
	} else if string(username) != expectedUsername {
		return fmt.Errorf("%w expected(%x) actual(%x)", errMismatchUsername, expectedUsername, string(username))
	}

	return nil
}
