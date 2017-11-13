package etcdircd

import (
	"strings"

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
	Topic(ch, msg string) error
	Away(msg string) error

	Close() error
}

func ClientDo(c Client, msg *irc.Message) error {
	glog.V(7).Infof("GOT %+v", msg)
	switch msg.Command {
	case irc.AWAY:
		awayMsg := ""
		if len(msg.Params) > 0 {
			if awayMsg = msg.Params[0]; awayMsg == "" {
				awayMsg = "User is away"
			}
		}
		return c.Away(awayMsg)
	case irc.CAP:
	case irc.WHOIS:
		return c.Whois(msg.Params[0])
	case irc.WHO:
		return c.Who(msg.Params)
	case irc.JOIN:
		// Parameters: ( <ch> *( "," <ch> ) [ <key> *( "," <key> ) ] ) / "0"
		if len(msg.Params) > 1 {
			glog.V(9).Infof("not supported %+v", msg)
		}
		return c.Join(msg.Params[0])
	case irc.MODE:
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
		partMsg := ""
		if len(msg.Params) > 1 {
			partMsg = msg.Params[1]
		}
		return c.Part(msg.Params[0], partMsg)
	case irc.QUIT:
		return c.Quit(msg.Params[0])
	case irc.PRIVMSG:
		return c.PrivMsg(msg.Params[0], msg.Params[1])
	case irc.TOPIC:
		return c.Topic(msg.Params[0], msg.Params[1])
	}
	return nil
}
