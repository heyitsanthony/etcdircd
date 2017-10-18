package etcdircd

import (
	"fmt"
	"net"
	"sync"
	"time"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/golang/glog"
	"gopkg.in/sorcix/irc.v2"
)

type Server struct {
	ln      net.Listener
	conns   map[*Conn]struct{}
	created time.Time

	cli     *etcd.Client
	ses     *etcdSession
	cfg     Config
	hostPfx irc.Prefix
}

func NewServer(cfg Config, ln net.Listener, cli *etcd.Client) *Server {
	s := &Server{
		ln:      ln,
		cli:     cli,
		ses:     newEtcdSession(cli),
		conns:   make(map[*Conn]struct{}),
		created: time.Now(),
		cfg:     cfg,
	}
	s.hostPfx = irc.Prefix{Name: s.cfg.HostName}
	return s
}

func (s *Server) Close() {
	s.ses.Close()
}

func (s *Server) Serve() (err error) {
	defer s.Close()
	var wg sync.WaitGroup
	for {
		c, cerr := s.ln.Accept()
		if cerr != nil {
			err = cerr
			glog.V(9).Infof("server shutdown: %v", err)
			break
		}
		wg.Add(1)
		go func() {
			defer func() {
				c.Close()
				wg.Done()
			}()
			s.handleConn(c)
		}()
	}
	wg.Wait()
	return err
}

func (s *Server) handleConn(c net.Conn) {
	cn := NewConn(c)
	cr := s.readConnRequest(cn)
	if cr == nil {
		return
	}
	cli, err := s.handshake(cn, cr)
	if err != nil {
		return
	}
	defer func() {
		cli.Close()
		glog.V(9).Infof("disconnected nick %v", cr.nick)
	}()
	for {
		var err error
		select {
		case msg := <-cn.Reader():
			switch msg.Command {
			case irc.MOTD:
				err = s.motd(cr.nick, cli)
			default:
				err = ClientDo(cli, msg)
			}
		case err = <-cn.Err():
		case <-s.cli.Ctx().Done():
			err = s.cli.Ctx().Err()
		}
		if err != nil {
			glog.V(7).Infof("err %v", err)
			return
		}
	}
}

type connectRequest struct {
	nick string
	user []string
}

func (s *Server) handshake(cn *Conn, cr *connectRequest) (Client, error) {
	var cli Client
	nn, hn, version := s.cfg.NetworkName, s.cfg.HostName, "etcdircd-0.0.0"
	for {
		newCli, err := newEtcdClient(s, cn, cr)
		if newCli != nil {
			cli = newCli
			break
		}
		if err != nil {
			return nil, err
		}
		msg := irc.Message{&s.hostPfx, irc.ERR_NICKNAMEINUSE, []string{cr.nick}}
		cn.SendMsg(s.cli.Ctx(), msg)
		select {
		case rmsg := <-cn.Reader():
			switch rmsg.Command {
			case "CAP":
			case "NICK":
				cr.nick = rmsg.Params[0]
			default:
				return nil, fmt.Errorf("bad handshake: %+v", rmsg)
			}
		case <-s.cli.Ctx().Done():
			return nil, s.cli.Ctx().Err()
		}
	}

	// User[2] is the host the client wants to access.

	nnpfx := &irc.Prefix{Name: nn}
	uv := UserValue{Nick: cr.nick, User: cr.user}
	cli.SendMsg(s.cli.Ctx(), irc.Message{
		Prefix:  nnpfx,
		Command: irc.RPL_WELCOME,
		Params: []string{
			cr.nick,
			"Welcome to an Internet Relay Network " + uv.Nick + "!" + uv.User[1] + "@masked",
		},
	})
	cli.SendMsg(s.cli.Ctx(), irc.Message{
		Prefix:  nnpfx,
		Command: irc.RPL_YOURHOST,
		Params:  []string{cr.nick, "Your host is " + hn + ", running version " + version},
	})

	date := s.created.Format("Mon Jan 2 15:04:05 -0700 MST 2006")
	// 01:23:45 Jan 01 1900
	cli.SendMsg(s.cli.Ctx(), irc.Message{
		Prefix:  nnpfx,
		Command: irc.RPL_CREATED,
		Params:  []string{cr.nick, "This server was created " + date},
	})
	// cli modes; channel modes
	cli.SendMsg(s.cli.Ctx(), irc.Message{
		Prefix:  nnpfx,
		Command: irc.RPL_MYINFO,
		Params:  []string{cr.nick, nn + " " + version + " i i"},
	})
	if s.cfg.Motd != "" {
		s.motd(cr.nick, cli)
	}
	cli.Send(s.cli.Ctx(), "PING", "hello")
	return cli, s.cli.Ctx().Err()
}

func (s *Server) motd(nick string, cli Client) error {
	if s.cfg.Motd == "" {
		return cli.SendMsg(s.cli.Ctx(), irc.Message{
			Prefix:  &s.hostPfx,
			Command: irc.ERR_NOMOTD,
			Params:  []string{nick, "MOTD file is missing"},
		})
	}

	cli.SendMsg(s.cli.Ctx(), irc.Message{
		Prefix:  &s.hostPfx,
		Command: irc.RPL_MOTDSTART,
		Params:  []string{nick, "- " + s.cfg.HostName + " Message of the day - "},
	})
	cli.SendMsg(s.cli.Ctx(), irc.Message{
		Prefix:  &s.hostPfx,
		Command: irc.RPL_MOTD,
		Params:  []string{nick, s.cfg.Motd},
	})
	return cli.SendMsg(s.cli.Ctx(), irc.Message{
		Prefix:  &s.hostPfx,
		Command: irc.RPL_ENDOFMOTD,
		Params:  []string{nick, "End of MOTD command"},
	})
}

func (s *Server) readConnRequest(c *Conn) *connectRequest {
	cr := &connectRequest{}
	gotCap := false
	for msg := range c.Reader() {
		switch msg.Command {
		case "CAP":
			gotCap = gotCap || msg.Params[0] == "LS"
		case "NICK":
			cr.nick = msg.Params[0]
		case "USER":
			cr.user = msg.Params
		}
		if cr.nick != "" && cr.user != nil {
			if gotCap {
				c.Writer() <- irc.Message{
					Command: ":servname CAP",
					Params:  []string{cr.nick, "LS", "[]"},
				}
			}
			return cr
		}
	}
	return nil
}
