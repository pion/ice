package ice

import (
	"net"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

func TestListenUDP(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

	localIPs, err := a.localInterfaces([]NetworkType{NetworkTypeUDP4})
	if len(localIPs) == 0 {
		t.Fatal("localInterfaces found no interfaces, unable to test")
	} else if err != nil {
		t.Fatal(err)
	}

	ip := localIPs[0]

	conn, err := a.listenUDP(0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
	if err != nil {
		t.Fatalf("listenUDP error with no port restriction %v", err)
	} else if conn == nil {
		t.Fatalf("listenUDP error with no port restriction return a nil conn")
	}

	_, err = a.listenUDP(4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	if err == nil {
		t.Fatal("listenUDP with invalid port range did not fail")
	}
	if err != ErrPort {
		t.Fatal("listenUDP with invalid port range did not return ErrPort")
	}

	conn, err = a.listenUDP(5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
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

	portMin := 5100
	portMax := 5109
	total := portMax - portMin + 1
	result := make([]int, 0, total)
	portRange := make([]int, 0, total)
	for i := 0; i < total; i++ {
		conn, err = a.listenUDP(portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
		if err != nil {
			t.Fatalf("listenUDP error with no port restriction %v", err)
		} else if conn == nil {
			t.Fatalf("listenUDP error with no port restriction return a nil conn")
		}

		_, port, err = net.SplitHostPort(conn.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		p, _ := strconv.Atoi(port)
		if p < portMin || p > portMax {
			t.Fatalf("listenUDP with port restriction [%d, %d] listened on incorrect port (%s)", portMin, portMax, port)
		}
		result = append(result, p)
		portRange = append(portRange, portMin+i)
	}
	if sort.IntsAreSorted(result) {
		t.Fatalf("listenUDP with port restriction [%d, %d], ports result should be random", portMin, portMax)
	}
	sort.Ints(result)
	if !reflect.DeepEqual(result, portRange) {
		t.Fatalf("listenUDP with port restriction [%d, %d], got:%v, want:%v", portMin, portMax, result, portRange)
	}
	_, err = a.listenUDP(portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
	if err == nil {
		t.Fatalf("listenUDP with port restriction [%d, %d], should return error", portMin, portMax)
	}
	if err != ErrPort {
		t.Fatalf("listenUDP with port restriction [%d, %d], did not return ErrPort", portMin, portMax)
	}
}
