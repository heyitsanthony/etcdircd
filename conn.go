package etcdircd

import (
	"context"
	"io"
	"sync"

	"github.com/golang/glog"
	"gopkg.in/sorcix/irc.v2"
)

type Conn struct {
	irc *irc.Conn
	rwc io.ReadWriteCloser

	rch  chan *irc.Message
	wch  chan irc.Message
	errc chan error

	stopc chan struct{}
	wg    sync.WaitGroup

	// wmu protects encoder writes
	wmu sync.Mutex
}

func NewConn(rwc io.ReadWriteCloser) *Conn {
	c := &Conn{
		irc:   irc.NewConn(rwc),
		rwc:   rwc,
		rch:   make(chan *irc.Message, 1024),
		wch:   make(chan irc.Message),
		errc:  make(chan error, 2),
		stopc: make(chan struct{}),
	}
	c.wg.Add(2)
	go c.read()
	go c.write()
	return c
}

type ConnIRC interface {
	Send(ctx context.Context, cmd string, params ...string) error
	SendMsg(ctx context.Context, msg irc.Message) error
	SendMsgSync(msg irc.Message) error
}

func (c *Conn) SendMsg(ctx context.Context, msg irc.Message) error {
	glog.V(9).Infof("sending msg %q", msg.String())
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.Writer() <- msg:
		return nil
	}
}
func (c *Conn) SendMsgSync(msg irc.Message) error {
	c.wmu.Lock()
	err := c.irc.Encode(&msg)
	c.wmu.Unlock()
	return err
}

func (c *Conn) Send(ctx context.Context, cmd string, params ...string) error {
	return c.SendMsg(ctx, irc.Message{Command: cmd, Params: params})
}

func (c *Conn) Reader() <-chan *irc.Message { return c.rch }
func (c *Conn) Writer() chan<- irc.Message  { return c.wch }
func (c *Conn) Err() <-chan error           { return c.errc }

func (c *Conn) Close() {
	close(c.stopc)
	c.rwc.Close()
	c.wg.Wait()
	close(c.errc)
}

func (c *Conn) read() {
	defer c.wg.Done()
	for {
		d, err := c.irc.Decode()
		if err != nil {
			c.errc <- err
			return
		}
		select {
		case c.rch <- d:
		case <-c.stopc:
			return
		}
	}
}

func (c *Conn) write() {
	defer c.wg.Done()
	for {
		select {
		case w := <-c.wch:
			c.wmu.Lock()
			err := c.irc.Encode(&w)
			c.wmu.Unlock()
			if err != nil {
				c.errc <- err
				return
			}
		case <-c.stopc:
			return
		}
	}
}
