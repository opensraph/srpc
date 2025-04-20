package srpc

import (
	"net"

	"google.golang.org/grpc/credentials"
)

var _ net.Listener = (*listener)(nil)

type listener struct {
	net.Listener
	creds credentials.TransportCredentials
}

// Accept waits for and returns the next incoming TLS connection.
// The returned connection is of type *Conn.
func (l *listener) Accept() (net.Conn, error) {
	if l.Listener == nil {
		return nil, nil
	}

	rawConn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	if l.creds != nil {
		c, _, err := l.creds.ServerHandshake(rawConn)
		if err != nil {
			rawConn.Close()
			return nil, err
		}
		rawConn = c
	}

	return rawConn, nil
}

// Close closes the listener.
func (l *listener) Close() error {
	if l.Listener == nil {
		return nil
	}

	return l.Listener.Close()
}

// Addr returns the listener's network address.
func (l *listener) Addr() net.Addr {
	if l.Listener == nil {
		return nil
	}

	return l.Listener.Addr()
}
