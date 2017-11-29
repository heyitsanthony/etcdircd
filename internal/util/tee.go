package util

import (
	"io"
	"net"
	"os"
	"path/filepath"
)

type teeListener struct {
	net.Listener
	dir string
}

func TeeListener(ln net.Listener, dir string) net.Listener {
	return &teeListener{ln, dir}
}

func (tl *teeListener) Accept() (net.Conn, error) {
	cn, err := tl.Listener.Accept()
	if err != nil {
		return nil, err
	}
	name := cn.RemoteAddr().String() + "_" + cn.LocalAddr().String() + ".out"
	fl := os.O_WRONLY | os.O_APPEND | os.O_CREATE
	f, err := os.OpenFile(filepath.Join(tl.dir, name), fl, 0600)
	if err != nil {
		cn.Close()
		return nil, err
	}
	return &teeConn{cn, io.TeeReader(cn, f), f}, nil
}

type teeConn struct {
	net.Conn
	r io.Reader
	c io.Closer
}

func (tc *teeConn) Read(b []byte) (int, error) { return tc.r.Read(b) }

func (tc *teeConn) Close() error {
	tc.c.Close()
	return tc.Conn.Close()
}
