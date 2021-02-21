package ice

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"net"
	"sync"
	"testing"
	"time"
)

func TestCandidateHostMuxed_setIP(t *testing.T) {
	receivedMessages := []string{}
	sendMessages := []string{}
	for i := 1; i < 20000; i++ {
		sendMessages = append(sendMessages, fmt.Sprintf("Message ID: %d", i))
	}
	sendMessages = append(sendMessages, "end")


	port := 56767
	StartUdpMuxerService(port)

	muxedConn, err := NewUdpMuxedConnection()
	assert.NoError(t, err, "Unable to create muxed udp connection")

	wg := sync.WaitGroup{}
	go func() {
		wg.Add(1)
		defer wg.Done()

		buffer := make([]byte, receiveMTU)
		for {
			n, src, err := muxedConn.ReadFrom(buffer)
			assert.NoError(t, err)
			assert.NotEqual(t, n, 0, "Zero bytes read")

			message := string(buffer[:n])
			fmt.Printf("Read bytes: %d, from src: %s, data: %s \n", n, src.String(), message)
			receivedMessages = append(receivedMessages, message)

			if message == "end" {
				break
			}
		}
	}()

	time.Sleep(1 * time.Second)

	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	assert.NoError(t, err, "unable to create upd client")

	fmt.Printf("Local Address: %s \n", conn.LocalAddr())
	n, err := muxedConn.WriteTo([]byte("Hello"), conn.LocalAddr())
	assert.NoError(t, err, "Unable to send data to open conn")
	assert.NotEqual(t, n, 0, "Zero bytes writtien")

	for _, msg := range sendMessages {
		fmt.Fprintf(conn, msg)
	}

	conn.Close()
	wg.Wait()

	assert.Equal(t, len(sendMessages), len(receivedMessages), "Length send messages != length of receive messages")
	for i, msg := range sendMessages {
		assert.Equal(t, msg, receivedMessages[i], "Messages dont match")
	}
}
