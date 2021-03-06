package unix

import (
	"net"
	"context"
	"library/binlog"
)
const (
	CMD_STOP = 1
	CMD_RELOAD = 2
)
type UnixClient struct {
	addr string
	conn *net.Conn
}

type UnixServer struct {
	addr string
	cancel *context.CancelFunc
	binlog *binlog.Binlog
	pidFile string
}
