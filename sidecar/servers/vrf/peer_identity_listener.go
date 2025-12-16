package vrf

import (
	"net"
	"syscall"

	"go.uber.org/zap"
)

type peerIdentityAddr struct {
	network string
	key     string
}

func (a peerIdentityAddr) Network() string { return a.network }
func (a peerIdentityAddr) String() string  { return a.key }

func (a peerIdentityAddr) ClientKey() string {
	switch a.network {
	case "unix":
		return "uds:" + a.key
	default:
		return a.network + ":" + a.key
	}
}

type peerIdentityConn struct {
	net.Conn
	remote net.Addr
}

func (c *peerIdentityConn) RemoteAddr() net.Addr { return c.remote }

func (c *peerIdentityConn) SyscallConn() (syscall.RawConn, error) {
	sysConn, ok := c.Conn.(syscall.Conn)
	if !ok {
		return nil, errConnNoRawFD
	}
	return sysConn.SyscallConn()
}

type peerIdentityListener struct {
	net.Listener
	logger *zap.Logger
}

func (l *peerIdentityListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	key, err := udsPeerToken(conn)
	if err != nil {
		l.logger.Debug("failed to determine UDS peer identity", zap.Error(err))
		return conn, nil
	}
	if key == "" {
		return conn, nil
	}

	return &peerIdentityConn{
		Conn:   conn,
		remote: peerIdentityAddr{network: "unix", key: key},
	}, nil
}

func withPerClientUDSPeerIdentity(ln net.Listener, logger *zap.Logger) net.Listener {
	if ln == nil {
		return nil
	}

	if ln.Addr() == nil || ln.Addr().Network() != "unix" {
		return ln
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	return &peerIdentityListener{
		Listener: ln,
		logger:   logger,
	}
}

func udsPeerToken(conn net.Conn) (string, error) {
	if conn == nil {
		return "", errNilConn
	}

	sysConn, ok := conn.(syscall.Conn)
	if !ok {
		return "", errConnNoRawFD
	}

	rawConn, err := sysConn.SyscallConn()
	if err != nil {
		return "", err
	}

	var (
		key        string
		controlErr error
	)
	if err := rawConn.Control(func(fd uintptr) {
		key, controlErr = udsPeerTokenFromFD(int(fd))
	}); err != nil {
		return "", err
	}
	if controlErr != nil {
		return "", controlErr
	}

	return key, nil
}
