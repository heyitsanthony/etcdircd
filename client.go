package etcdircd

import (
	"strings"
	"sync"

	"github.com/golang/glog"
	"gopkg.in/sorcix/irc.v2"
)

// Client accepts messages from a connected IRC client.
type Client interface {
	ConnIRC

	Nick(n string) error
	Mode(m []string) error
	Join(ch string) error
	List(ch []string) error
	Names(ch string) error
	Ping(msg string) error
	Part(ch, msg string) error
	Quit(msg string) error
	Whois(n string) error
	Who(args []string) error
	PrivMsg(target, msg string) error
	Topic(ch string, msg *string) error
	Away(msg string) error
	Oper(login, pass string) error
	Die() error
	Kick(ch string, nicks []string, msg string) error

	Close() error

	sendHostMsg(cmd string, params ...string) error
}

func ClientDo(c Client, msg *irc.Message) error {
	glog.V(7).Infof("GOT %+v", msg)
	switch msg.Command {
	case irc.KICK:
		if len(msg.Params) < 2 {
			return needMoreParams(c)
		}
		ch := msg.Params[0]
		if !isChan(ch) {
			return badChanMask(c, ch)
		}
		nicks := strings.Split(msg.Params[1], ",")
		kickMsg := ""
		if len(msg.Params) >= 3 {
			kickMsg = msg.Params[2]
		}
		return c.Kick(ch, nicks, kickMsg)
	case irc.DIE:
		return c.Die()
	case irc.AWAY:
		awayMsg := ""
		if len(msg.Params) > 0 {
			if awayMsg = msg.Params[0]; awayMsg == "" {
				awayMsg = "User is away"
			}
		}
		return c.Away(awayMsg)
	case irc.OPER:
		if len(msg.Params) < 2 {
			return needMoreParams(c)
		}
		return c.Oper(msg.Params[0], msg.Params[1])
	case irc.CAP:
	case irc.WHOIS:
		n := ""
		if len(msg.Params) > 0 {
			n = msg.Params[0]
			if isChan(n) {
				glog.V(9).Infof("whois: no such nick %q", n)
				return noSuchNick(c, n)
			}
		}
		return c.Whois(n)
	case irc.WHO:
		return c.Who(msg.Params)
	case irc.JOIN:
		// Parameters: ( <ch> *( "," <ch> ) [ <key> *( "," <key> ) ] ) / "0"
		if len(msg.Params) == 0 {
			return needMoreParams(c)
		}
		if len(msg.Params) > 1 {
			glog.V(9).Infof("not supported %+v", msg)
		}
		chs := strings.Split(msg.Params[0], ",")
		for i := range chs {
			if !isChan(chs[i]) {
				return noSuchChannel(c, chs[i])
			}
		}
		errc := make(chan error, len(chs))
		var wg sync.WaitGroup
		wg.Add(len(chs))
		for i := range chs {
			go func(ch string) {
				defer wg.Done()
				if err := c.Join(ch); err != nil {
					errc <- err
				}
			}(chs[i])
		}
		wg.Wait()
		close(errc)
		return <-errc
	case irc.MODE:
		if len(msg.Params) == 0 {
			return needMoreParams(c)
		}
		return c.Mode(msg.Params)
	case irc.LIST:
		// Parameters: [ <channel> *( "," <channel> ) [ <target> ] ]
		args := []string{}
		if len(msg.Params) > 0 {
			args = strings.Split(msg.Params[0], ",")
		}
		return c.List(args)
	case irc.NAMES:
		// Command: NAMES
		// Parameters: [ <channel> *( "," <channel> ) [ <target> ] ]
		return c.Names(msg.Params[0])
	case irc.PING:
		return c.Ping(msg.Params[0])
	case irc.PART:
		if len(msg.Params) == 0 {
			return needMoreParams(c)
		}
		ch := msg.Params[0]
		if !isChan(ch) {
			return noSuchChannel(c, ch)
		}
		partMsg := ""
		if len(msg.Params) > 1 {
			partMsg = msg.Params[1]
		}
		return c.Part(ch, partMsg)
	case irc.QUIT:
		return c.Quit(msg.Params[0])
	case irc.PRIVMSG:
		return c.PrivMsg(msg.Params[0], msg.Params[1])
	case irc.TOPIC:
		if len(msg.Params) < 1 {
			return needMoreParams(c)
		}
		ch := msg.Params[0]
		if !isChan(ch) {
			return badChanMask(c, ch)
		}
		var topic *string
		if len(msg.Params) > 1 {
			topic = &msg.Params[1]
		}
		return c.Topic(ch, topic)
	}
	return nil
}

func needMoreParams(c Client) error {
	return c.sendHostMsg(irc.ERR_NEEDMOREPARAMS, "Need more parameters")
}

func badChanMask(c Client, ch string) error {
	return c.sendHostMsg(irc.ERR_BADCHANMASK, ch, "Bad Channel Mask")
}

func noSuchChannel(c Client, ch string) error {
	return c.sendHostMsg(irc.ERR_NOSUCHCHANNEL, ch, "No such channel")
}

func noSuchNick(c Client, n string) error {
	return c.sendHostMsg(irc.ERR_NOSUCHNICK, n, "No such nick/channel")
}
