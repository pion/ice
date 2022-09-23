//go:build js

package ice

import (
	"errors"
	"net"

	"github.com/pion/logging"
)

var errUnsupported = errors.New("unsupported")

func setUDPSocketOptionsForLocalAddr(fd uintptr, logger logging.LeveledLogger) {
}

func getLocalAddrFromOob(oob []byte) (net.IP, error) {
	return nil, errUnsupported
}
