// +build !js

package ice

//nolint:gosec
import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/test"
	"github.com/stretchr/testify/require"
)

func TestUDPMux(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	loggerFactory := logging.NewDefaultLoggerFactory()
	udpMux := NewUDPMuxDefault(UDPMuxParams{
		Logger: loggerFactory.NewLogger("ice"),
	})
	err := udpMux.Start(7686)
	require.NoError(t, err)

	defer func() {
		_ = udpMux.Close()
	}()

	require.NotNil(t, udpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")
	require.Equal(t, ":7686", udpMux.LocalAddr().String())

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxConnection(t, udpMux, "ufrag1")
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxConnection(t, udpMux, "ufrag2")
	}()

	testMuxConnection(t, udpMux, "ufrag3")
	wg.Wait()

	require.NoError(t, udpMux.Close())

	// can't create more connections
	_, err = udpMux.GetConn("failufrag", "udp")
	require.Error(t, err)
}

func TestAddressEncoding(t *testing.T) {
	cases := []struct {
		name string
		addr net.UDPAddr
	}{
		{
			name: "empty address",
		},
		{
			name: "ipv4",
			addr: net.UDPAddr{
				IP:   net.IPv4(244, 120, 0, 5),
				Port: 6000,
				Zone: "",
			},
		},
		{
			name: "ipv6",
			addr: net.UDPAddr{
				IP:   net.IPv6loopback,
				Port: 2500,
				Zone: "zone",
			},
		},
	}

	for _, c := range cases {
		addr := c.addr
		t.Run(c.name, func(t *testing.T) {
			buf := make([]byte, maxAddrSize)
			n, err := encodeUDPAddr(&addr, buf)
			require.NoError(t, err)

			parsedAddr, err := decodeUDPAddr(buf[:n])
			require.NoError(t, err)
			require.EqualValues(t, &addr, parsedAddr)
		})
	}
}

func testMuxConnection(t *testing.T, udpMux *UDPMuxDefault, ufrag string) {
	pktConn, err := udpMux.GetConn(ufrag, udp)
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	remoteConn, err := net.DialUDP(udp, nil, udpMux.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err, "error dialing test udp connection")

	// initial messages are dropped
	_, err = remoteConn.Write([]byte("dropped bytes"))
	require.NoError(t, err)
	// wait for packet to be consumed
	time.Sleep(time.Millisecond)

	// write out to establish connection
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte(ufrag+":otherufrag"))
	msg.Encode()
	_, err = pktConn.WriteTo(msg.Raw, remoteConn.LocalAddr())
	require.NoError(t, err)

	// ensure received
	buf := make([]byte, receiveMTU)
	n, err := remoteConn.Read(buf)
	require.NoError(t, err)
	require.Equal(t, msg.Raw, buf[:n])

	// start writing packets through mux
	targetSize := 1 * 1024 * 1024
	readDone := make(chan struct{}, 1)

	// read packets from the muxed side
	go func() {
		defer func() {
			t.Logf("closing read chan for: %s", ufrag)
			close(readDone)
		}()
		readBuf := make([]byte, receiveMTU)
		nextSeq := uint32(0)
		for read := 0; read < targetSize; {
			n, _, _ := pktConn.ReadFrom(readBuf)
			require.NoError(t, err)
			require.Equal(t, receiveMTU, n)

			verifyPacket(t, readBuf, nextSeq)

			read += n
			nextSeq++
		}
	}()

	sequence := 0
	for written := 0; written < targetSize; {
		buf := make([]byte, receiveMTU)
		// byte0-4: sequence
		// bytes4-24: sha1 checksum
		// bytes24-mtu: random data
		_, err := rand.Read(buf[24:])
		require.NoError(t, err)
		h := sha1.Sum(buf[24:]) //nolint:gosec
		copy(buf[4:24], h[:])
		binary.LittleEndian.PutUint32(buf[0:4], uint32(sequence))

		_, err = remoteConn.Write(buf)
		require.NoError(t, err)

		written += len(buf)
		sequence++

		time.Sleep(time.Millisecond)
	}

	<-readDone
}

func verifyPacket(t *testing.T, b []byte, nextSeq uint32) {
	readSeq := binary.LittleEndian.Uint32(b[0:4])
	require.Equal(t, nextSeq, readSeq)
	h := sha1.Sum(b[24:]) //nolint:gosec
	require.Equal(t, h[:], b[4:24])
}
