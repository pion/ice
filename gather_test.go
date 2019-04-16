package ice

import (
	"net"
	"testing"
)

func TestListenUDP(t *testing.T) {
	localIPs := localInterfaces([]NetworkType{NetworkTypeUDP4})
	if len(localIPs) == 0 {
		t.Fatal("localInterfaces found no interfaces, unable to test")
	}

	ip := localIPs[0]

	conn, err := listenUDP(0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
	if err != nil {
		t.Fatalf("listenUDP error with no port restriction %v", err)
	} else if conn == nil {
		t.Fatalf("listenUDP error with no port restriction return a nil conn")
	}

	_, err = listenUDP(500, 499, udp, &net.UDPAddr{IP: ip, Port: 0})
	if err != ErrPort {
		t.Fatal("listenUDP with invalid port range did not return ErrPort")
	}

	conn, err = listenUDP(5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	if err != nil {
		t.Fatalf("listenUDP error with no port restriction %v", err)
	} else if conn == nil {
		t.Fatalf("listenUDP error with no port restriction return a nil conn")
	}

	_, port, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	} else if port != "5000" {
		t.Fatalf("listenUDP with port restriction of 5000 listened on incorrect port (%s)", port)
	}
}
