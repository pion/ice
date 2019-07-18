package ice

import (
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/transport/vnet"
)

func TestVNetGather(t *testing.T) {
	loggerFactory := logging.NewDefaultLoggerFactory()
	//log := loggerFactory.NewLogger("test")

	t.Run("No local IP address", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{
			Net: vnet.NewNet(&vnet.NetConfig{}),
		})
		if err != nil {
			t.Fatalf("Failed to create agent: %s", err)
		}

		localIPs, err := a.localInterfaces([]NetworkType{NetworkTypeUDP4})
		if len(localIPs) > 0 {
			t.Fatal("should return no local IP")
		} else if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Gather a dynamic IP address", func(t *testing.T) {
		cider := "1.2.3.0/24"
		_, ipNet, err := net.ParseCIDR(cider)
		if err != nil {
			t.Fatalf("Failed to parse CIDR: %s", err)
		}

		r, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          cider,
			LoggerFactory: loggerFactory,
		})
		if err != nil {
			t.Fatalf("Failed to create a router: %s", err)
		}

		nw := vnet.NewNet(&vnet.NetConfig{})
		if nw == nil {
			t.Fatalf("Failed to create a Net: %s", err)
		}

		err = r.AddNet(nw)
		if err != nil {
			t.Fatalf("Failed to add a Net to the router: %s", err)
		}

		a, err := NewAgent(&AgentConfig{
			Net: nw,
		})
		if err != nil {
			t.Fatalf("Failed to create agent: %s", err)
		}

		localIPs, err := a.localInterfaces([]NetworkType{NetworkTypeUDP4})
		if len(localIPs) == 0 {
			t.Fatal("should have one local IP")
		} else if err != nil {
			t.Fatal(err)
		}

		for _, ip := range localIPs {
			if ip.IsLoopback() {
				t.Fatal("should not return loopback IP")
			}
			if !ipNet.Contains(ip) {
				t.Fatal("should be contained in the CIDR")
			}
		}
	})

	t.Run("listenUDP", func(t *testing.T) {
		r, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          "1.2.3.0/24",
			LoggerFactory: loggerFactory,
		})
		if err != nil {
			t.Fatalf("Failed to create a router: %s", err)
		}

		nw := vnet.NewNet(&vnet.NetConfig{})
		if nw == nil {
			t.Fatalf("Failed to create a Net: %s", err)
		}

		err = r.AddNet(nw)
		if err != nil {
			t.Fatalf("Failed to add a Net to the router: %s", err)
		}

		a, err := NewAgent(&AgentConfig{Net: nw})
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
	})
}
