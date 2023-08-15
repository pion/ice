package ice

import (
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"golang.org/x/net/ipv4"
)

const (
	sendMTU = 1500
)

type batchWriter interface {
	WriteBatch(ms []ipv4.Message, flags int) (int, error)
}

type BatchConnWriter struct {
	net.PacketConn

	logger logging.LeveledLogger
	writer batchWriter

	batchWriteMutex    sync.Mutex
	batchWriteMessages []ipv4.Message
	batchWritePos      int
	batchWriteLast     time.Time

	// readBatchSize      int
	// todo : variable batch write size based on past average write speed (pps)
	batchWriteSize     int
	batchWriteInterval time.Duration

	closed atomic.Bool
}

func NewBatchConn(conn net.PacketConn, batchWriteSize int, batchWriteInterval time.Duration, logger logging.LeveledLogger) *BatchConnWriter {
	bc := &BatchConnWriter{
		PacketConn:         conn,
		batchWriteLast:     time.Now(),
		batchWriteInterval: batchWriteInterval,
		batchWriteSize:     batchWriteSize,
		batchWriteMessages: make([]ipv4.Message, batchWriteSize),
		logger:             logger,
	}
	for i := range bc.batchWriteMessages {
		bc.batchWriteMessages[i].Buffers = [][]byte{make([]byte, sendMTU)}
	}

	if runtime.GOOS == "linux" {
		if pc4 := ipv4.NewPacketConn(conn); pc4 != nil {
			bc.writer = pc4
		} else if pc6 := ipv4.NewPacketConn(conn); pc6 != nil {
			bc.writer = pc6
		} else {
			bc.logger.Warn("failed to create ipv4 or ipv6 packet conn, can't use batch write")
		}
	} else {
		bc.logger.Warn("batch write only supports linux")
	}

	if bc.writer != nil {
		go func() {
			tk := time.NewTicker(batchWriteInterval / 2)
			defer tk.Stop()
			for !bc.closed.Load() {
				<-tk.C
				bc.batchWriteMutex.Lock()
				if bc.batchWritePos > 0 && time.Since(bc.batchWriteLast) > bc.batchWriteInterval {
					bc.flush()
				}
				bc.batchWriteMutex.Unlock()
			}
		}()
	}

	return bc
}

func (c *BatchConnWriter) Close() error {
	c.closed.Store(true)
	return c.PacketConn.Close()
}

// func (c *BatchConn) Write(b []byte) (int, error) {
// 	if c.batchConn == nil {
// 		return c.conn.Write(b)
// 	}
// 	return c.writeBatch(b, nil)
// }

func (c *BatchConnWriter) WriteTo(b []byte, addr net.Addr) (int, error) {
	if c.writer == nil {
		return c.PacketConn.WriteTo(b, addr)
	}
	return c.writeBatch(b, addr)
}

func (c *BatchConnWriter) writeBatch(buf []byte, raddr net.Addr) (int, error) {
	c.batchWriteMutex.Lock()
	defer c.batchWriteMutex.Unlock()

	msg := &c.batchWriteMessages[c.batchWritePos]
	// reset buffers
	msg.Buffers = msg.Buffers[:1]
	msg.Buffers[0] = msg.Buffers[0][:cap(msg.Buffers[0])]

	c.batchWritePos++
	if raddr != nil {
		msg.Addr = raddr
	}
	if n := copy(msg.Buffers[0], buf); n < len(buf) {
		// todo: use extra buffer to copy remaining bytes
	} else {
		msg.Buffers[0] = msg.Buffers[0][:n]
	}
	if c.batchWritePos == c.batchWriteSize {
		c.flush()
	}
	return len(buf), nil
}

func (c *BatchConnWriter) flush() {
	var txN int
	for txN < c.batchWritePos {
		if n, err := c.writer.WriteBatch(c.batchWriteMessages[txN:c.batchWritePos], 0); err != nil {
			c.logger.Warnf("write error: %s, len(msgs): %d", err, len(c.batchWriteMessages[txN:c.batchWritePos]))
			break
		} else {
			txN += n
		}
	}
	c.batchWritePos = 0
}
