package etcdircd

import (
	"fmt"

	"context"
	"strings"
	"sync"
	"time"

	etcd "github.com/coreos/etcd/clientv3"
	v3sync "github.com/coreos/etcd/clientv3/concurrency"
	"github.com/golang/glog"
	"gopkg.in/sorcix/irc.v2"
)

type etcdClient struct {
	ConnIRC
	s *Server

	// crev is the creation revision of this user
	crev int64
	// nick is the registered nick
	nick string
	// name, username, hostname
	prefix irc.Prefix

	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	chCancel map[string]context.CancelFunc
	wg       sync.WaitGroup
}

func newEtcdClient(s *Server, cn ConnIRC, cr *connectRequest) (*etcdClient, error) {
	uv := UserValue{Nick: cr.nick, User: cr.user, Created: time.Now()}
	uv.Mode = newModeValue(s.cfg.PinnedUserModes)

	ctx, cancel := context.WithTimeout(s.cli.Ctx(), 5*time.Second)
	ss, err := s.ses.Session()
	if err != nil {
		return nil, err
	}
	opt := etcd.WithLease(ss.Lease())
	resp, err := s.cli.Txn(ctx).If(
		etcd.Compare(etcd.Version(keyUserCtl(uv.Nick)), "=", 0),
	).Then(
		etcd.OpPut(keyUserCtl(uv.Nick), encodeUserValue(uv), opt),
		etcd.OpPut(keyUserMsg(uv.Nick), "", opt),
	).Commit()
	cancel()
	if err != nil {
		return nil, err
	}
	if resp.Succeeded == false {
		return nil, nil
	}

	ctx, cancel = context.WithCancel(s.cli.Ctx())
	ec := &etcdClient{
		ConnIRC:  cn,
		s:        s,
		crev:     resp.Header.Revision,
		nick:     uv.Nick,
		prefix:   irc.Prefix{Name: uv.Nick, User: uv.User[1], Host: "host"},
		ctx:      ctx,
		cancel:   cancel,
		chCancel: make(map[string]context.CancelFunc),
	}

	// Accept privmsg on msgs only after "" initialization.
	msgc := ec.s.cli.Watch(ec.ctx, keyUserMsg(ec.nick), etcd.WithRev(resp.Header.Revision+1))
	ec.wg.Add(1)
	go func() {
		defer ec.wg.Done()
		err := ec.monitorMsg(msgc)
		glog.V(9).Infof("monitorMsg: %v; exiting", err)
		cancel()
		for range msgc {
		}
	}()

	return ec, nil
}

// monitorPart waits for deletions on a channel's nicks prefix and reports the lost
// nicks as leaves to the other parties in the channel.
func (ec *etcdClient) monitorPart(ch string, nicksc etcd.WatchChan) error {
	for resp := range nicksc {
		if err := resp.Err(); err != nil {
			return err
		}
		ss, err := ec.s.ses.Session()
		if err != nil {
			return err
		}
		lid := int64(ss.Lease())
		for _, ev := range resp.Events {
			if ev.Type == etcd.EventTypePut {
				// Delete if no keys left and owner.
				cns, err := decodeChannelNicks(string(ev.Kv.Value))
				if err != nil {
					glog.V(9).Infof("got error on decode %v", err)
					continue
				}
				if len(cns.Nicks) > 0 || ev.Kv.Lease != lid {
					continue
				}
				k := string(ev.Kv.Key)
				ec.s.cli.Txn(ec.ctx).If(
					etcd.Compare(etcd.ModRevision(k), "=", ev.Kv.ModRevision),
				).Then(
					etcd.OpDelete(k),
				).Commit()
				continue
			}
			// Nick key is deleted.
			cns, err := decodeChannelNicks(string(ev.PrevKv.Value))
			if err != nil {
				glog.V(9).Infof("got error on decode %v", err)
				continue
			}
			if err := ec.sendParts(ch, *cns); err != nil {
				return err
			}
		}
	}
	return ec.ctx.Err()
}

func (ec *etcdClient) sendParts(ch string, cns ChannelNicks) error {
	glog.V(9).Infof("sendLeaves: %q sending %+v", ch, cns)
	for _, cn := range cns.Nicks {
		msg := irc.Message{
			Prefix:  &irc.Prefix{Name: cn.Nick, User: cn.User, Host: cn.Host},
			Command: irc.PART,
			Params:  []string{ch, "session lost"},
		}
		if err := ec.SendMsg(ec.ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

func (ec *etcdClient) monitorMsg(msgc etcd.WatchChan) error {
	for resp := range msgc {
		if err := resp.Err(); err != nil {
			return err
		}
		for _, ev := range resp.Events {
			if ev.Type == etcd.EventTypeDelete {
				// maybe lost lease
				return ec.SendMsgSync(irc.Message{
					Prefix:  &ec.s.hostPfx,
					Command: irc.ERROR,
				})
			}
			msg, err := decodeMessage(string(ev.Kv.Value))
			if err != nil {
				glog.V(9).Infof("got error on decode %v", err)
				continue
			}
			if msg.Prefix != nil &&
				msg.Prefix.Name == ec.nick &&
				msg.Command == irc.PRIVMSG {
				glog.V(9).Infof("monitorMsg: dropping %+v", msg)
				continue
			}
			glog.V(9).Infof("monitorMsg: sending %+v", msg)
			if err := ec.SendMsg(ec.ctx, *msg); err != nil {
				return err
			}
			if msg.Command == irc.PART && msg.Prefix.Name == ec.nick {
				// remove watches on channel, should not receive more messages
				ch := msg.Params[0]
				ec.mu.Lock()
				if f := ec.chCancel[ch]; f != nil {
					f()
					delete(ec.chCancel, ch)
				}
				ec.mu.Unlock()
				return nil
			}
		}
	}
	return ec.ctx.Err()
}

func (ec *etcdClient) sendHostMsg(cmd string, params ...string) error {
	return ec.SendMsg(ec.ctx, irc.Message{&ec.s.hostPfx, cmd, params})
}

func (ec *etcdClient) Join(ch string) error {
	chs := strings.Split(ch, ",")
	errc := make(chan error, len(chs))
	var wg sync.WaitGroup
	wg.Add(len(chs))
	for i := range chs {
		go func(ch string) {
			defer wg.Done()
			if err := ec.join(ch); err != nil {
				errc <- err
			}
		}(chs[i])
	}
	wg.Wait()
	close(errc)
	return <-errc
}

func (ec *etcdClient) join(ch string) error {
	if !isChan(ch) {
		return ec.sendHostMsg(irc.ERR_NOSUCHCHANNEL, ch, "No such channel")
	}
	userCtl, chCtl := keyUserCtl(ec.nick), keyChanCtl(ch)
	ss, err := ec.s.ses.Session()
	if err != nil {
		return err
	}
	lid := ss.Lease()
	chNicks, chMsg := keyChanNicks(ch, int64(lid)), keyChanMsg(ch)

	msg := irc.Message{
		Prefix:  &ec.prefix,
		Command: irc.JOIN,
		Params:  []string{ch},
	}
	msgVal := encodeMessage(msg)

	// TODO: ERR_TOOMANYCHANNELS if too many channels
	topic := ""
	f := func(stm v3sync.STM) error {
		// Add channel to user.
		uv, err := decodeUserValue(stm.Get(userCtl))
		if err != nil {
			return err
		}
		uv.Channels = append(uv.Channels, ch)
		stm.Put(userCtl, encodeUserValue(*uv), etcd.WithLease(lid))

		// Create channel if it doesn't exist and fetch topic.
		chctlv := stm.Get(chCtl)
		var chv ChannelCtl
		if len(chctlv) == 0 {
			// channel does not exist
			chv.Name = ch
			chv.Created = time.Now()
		} else {
			chvv, err := decodeChannelCtl(chctlv)
			if err != nil {
				return err
			}
			chv = *chvv
		}
		topic = chv.Topic
		stm.Put(chCtl, encodeChannelCtl(chv))

		// Add user to server's channel nick list.
		var cns ChannelNicks
		nicks := stm.Get(chNicks)
		if len(nicks) > 0 {
			cn, err := decodeChannelNicks(nicks)
			if err != nil {
				return err
			}
			cns = *cn
		}
		cn := channelNick{
			Nick:       ec.nick,
			User:       ec.prefix.User,
			Host:       ec.prefix.Host,
			ServerName: ec.s.cfg.HostName,
			Realname:   "norton",
		}
		cns.Nicks = append(cns.Nicks, cn)
		nicks = encodeChannelNicks(cns)
		stm.Put(chNicks, nicks, etcd.WithLease(lid))

		// Broadcast the join to the channel members.
		stm.Put(chMsg, msgVal)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	resp, err := v3sync.NewSTM(
		ec.s.cli,
		f,
		v3sync.WithAbortContext(ctx),
		v3sync.WithIsolation(v3sync.Serializable),
		v3sync.WithPrefetch(userCtl, chCtl, chNicks),
	)
	cancel()
	if err != nil {
		return err
	}

	// TODO: if only nick, make channel operator

	// JOIN is successful; the user receives a JOIN message as confirmation.
	ec.SendMsg(ec.ctx, msg)
	// then sent the channel's topic (using RPL_TOPIC)
	if topic == "" {
		ec.sendHostMsg(irc.RPL_NOTOPIC, ec.nick, ch, "No topic is set")
	} else {
		ec.sendHostMsg(irc.RPL_TOPIC, ec.nick, ch, topic)
	}
	// and the list of users who are on the channel (using RPL_NAMREPLY)
	// Fetch nicks at time of join.
	if err := ec.names(ch, resp.Header.Revision); err != nil {
		return err
	}
	ec.monitorChannel(ch, resp.Header.Revision+1)
	return ec.ctx.Err()
}

func (ec *etcdClient) monitorChannel(ch string, rev int64) {
	ctx, cancel := context.WithCancel(ec.ctx)
	ec.mu.Lock()
	ec.chCancel[ch] = cancel
	ec.mu.Unlock()
	glog.V(9).Infof("monitoring channel %q", ch)
	ec.wg.Add(2)
	go func() {
		defer ec.wg.Done()
		msgc := ec.s.cli.Watch(ctx, keyChanMsg(ch), etcd.WithRev(rev))
		err := ec.monitorMsg(msgc)
		glog.V(9).Infof("monitorChannelMsg %q: %v; exiting", ch, err)
		for range msgc {
		}
	}()
	go func() {
		defer ec.wg.Done()
		msgc := ec.s.cli.Watch(
			ctx,
			keyChanNicksPfx(ch),
			etcd.WithRev(rev),
			etcd.WithPrefix(),
			etcd.WithPrevKV())
		err := ec.monitorPart(ch, msgc)
		glog.V(9).Infof("monitorChannelPfx %q: %v; exiting", ch, err)
		for range msgc {
		}
	}()
}

func (ec *etcdClient) names(ch string, rev int64) error {
	nickResp, err := ec.s.cli.Get(
		ec.ctx,
		keyChanNicksPfx(ch),
		etcd.WithPrefix(),
		etcd.WithRev(rev))
	if err != nil {
		return err
	}
	nicks := []string{}
	for _, serv := range nickResp.Kvs {
		cn, err := decodeChannelNicks(string(serv.Value))
		if err != nil {
			return err
		}
		for _, nick := range cn.Nicks {
			nicks = append(nicks, nick.Nick)
		}
	}
	ec.sendHostMsg(irc.RPL_NAMREPLY, ec.nick, "=", ch, strings.Join(nicks, " "))
	return ec.sendHostMsg(irc.RPL_ENDOFNAMES, ec.nick, ch, "End of NAMES list")
}

func (ec *etcdClient) Who(args []string) error {
	// The <mask> passed to WHO is matched against users' host, server, real
	// name and nickname if the channel <mask> cannot be found.
	if len(args) == 0 {
		return ec.sendHostMsg(irc.ERR_NEEDMOREPARAMS, "who", "Not enough parameters")
	}
	if isChan(args[0]) {
		return ec.whoChan(args[0])
	}
	return nil
}

func (ec *etcdClient) whoChan(ch string) error {
	resp, err := ec.s.cli.Get(ec.ctx, keyChanNicksPfx(ch), etcd.WithPrefix())
	if err != nil {
		return err
	}
	for _, kv := range resp.Kvs {
		nicks, err := decodeChannelNicks(string(kv.Value))
		if err != nil {
			return err
		}
		for _, n := range nicks.Nicks {
			v := fmt.Sprintf("%s %s %s %s %s %s H",
				ec.nick,
				ch,
				n.User,
				n.Host,
				n.ServerName,
				n.Nick)
			if err := ec.sendHostMsg(irc.RPL_WHOREPLY, v, "0 "+n.Realname); err != nil {
				return err
			}
		}
	}
	return ec.sendHostMsg(irc.RPL_ENDOFWHO, ec.nick, ch, "End of WHO list")
}

func (ec *etcdClient) Quit(msg string) error {
	userCtl := keyUserCtl(ec.nick)
	f := func(stm v3sync.STM) error {
		// Remove user from joined channels.
		uv, err := decodeUserValue(stm.Get(userCtl))
		if err != nil {
			return err
		}
		ss, err := ec.s.ses.Session()
		if err != nil {
			return err
		}
		lid := ss.Lease()
		for _, ch := range uv.Channels {
			// Remove from nicks in channel.
			chNicks := keyChanNicks(ch, int64(lid))
			nicks := stm.Get(chNicks)
			if len(nicks) == 0 {
				continue
			}
			cns, err := decodeChannelNicks(nicks)
			if err != nil {
				return err
			}
			for i, cnick := range cns.Nicks {
				if cnick.Nick == ec.nick {
					cns.Nicks = append(cns.Nicks[:i], cns.Nicks[i+1:]...)
					break
				}
			}
			stm.Put(chNicks, encodeChannelNicks(*cns), etcd.WithIgnoreLease())

			// Broadcast quit to channel.
			msgVal := encodeMessage(irc.Message{
				Prefix:  &ec.prefix,
				Command: irc.QUIT,
				Params:  []string{msg},
			})
			stm.Put(keyChanMsg(ch), msgVal)
		}
		// Delete user keys.
		stm.Del(userCtl)
		stm.Del(keyUserMsg(ec.nick))
		return nil
	}
	ctx, cancel := context.WithTimeout(ec.ctx, 5*time.Second)
	_, err := v3sync.NewSTM(
		ec.s.cli,
		f,
		v3sync.WithAbortContext(ctx),
		v3sync.WithIsolation(v3sync.Serializable),
		v3sync.WithPrefetch(userCtl),
	)
	cancel()
	if err != nil {
		glog.Warning(err)
		return err
	}
	return nil
}

func (ec *etcdClient) Mode(m []string) error {
	if len(m) == 0 {
		return ec.sendHostMsg(irc.ERR_NEEDMOREPARAMS, ec.nick)
	}
	if isChan(m[0]) {
		return ec.sendHostMsg(irc.ERR_NOCHANMODES, m[0], "Channel doesn't support modes")
	}
	if m[0] != ec.nick {
		return ec.sendHostMsg(irc.ERR_USERSDONTMATCH, ec.nick, "Cannot change mode for other users")
	}

	mode := ""
	bad := []byte{}

	userCtl := keyUserCtl(ec.nick)
	f := func(stm v3sync.STM) error {
		uv, err := decodeUserValue(stm.Get(userCtl))
		if err != nil {
			return err
		}
		for _, modeStr := range m[1:] {
			newMv, newBad := uv.Mode.update(modeStr)
			uv.Mode = newMv
			bad = append(bad, newBad...)
		}
		uv.Mode, _ = uv.Mode.update("+" + ec.s.cfg.PinnedUserModes)
		mode = string(uv.Mode)
		stm.Put(userCtl, encodeUserValue(*uv), etcd.WithIgnoreLease())
		return nil
	}
	_, err := v3sync.NewSTM(
		ec.s.cli,
		f,
		v3sync.WithAbortContext(ec.ctx),
		v3sync.WithIsolation(v3sync.Serializable),
		v3sync.WithPrefetch(userCtl),
	)
	if err != nil {
		return err
	}
	if len(m) == 1 {
		return ec.sendHostMsg(irc.RPL_UMODEIS, ec.nick, mode)
	}
	if err := ec.sendHostMsg(irc.MODE, ec.nick, mode); err != nil {
		return err
	}
	for _, m := range bad {
		err := ec.sendHostMsg(irc.ERR_UMODEUNKNOWNFLAG, ec.nick, string(m)+" is unknown mode char to me")
		if err != nil {
			return err
		}
	}
	return nil
}

func (ec *etcdClient) Ping(msg string) error {
	// time a server round-trip as part of the ping
	if _, err := ec.s.cli.Get(ec.ctx, keyUserCtl(ec.nick)); err != nil {
		return err
	}
	return ec.sendHostMsg(irc.PONG, ec.s.cfg.HostName, msg)
}

func (ec *etcdClient) PrivMsg(target, msg string) error {
	v := encodeMessage(irc.Message{
		Prefix:  &ec.prefix,
		Command: irc.PRIVMSG,
		Params:  []string{target, msg},
	})
	isOtrMsg := strings.HasPrefix(msg, "?OTR")

	keyCtl, keyMsg := "", ""
	if isChan(target) {
		keyCtl, keyMsg = keyChanCtl(target), keyChanMsg(target)
	} else {
		keyCtl, keyMsg = keyUserCtl(target), keyUserMsg(target)
	}
	myUserCtl := keyUserCtl(ec.nick)

	noNick, badOtr := false, false
	f := func(stm v3sync.STM) error {
		if noNick = stm.Rev(keyCtl) == 0; noNick {
			return nil
		}
		if !isOtrMsg && !isChan(target) {
			uv, err := decodeUserValue(stm.Get(myUserCtl))
			if err != nil {
				return err
			}
			if badOtr = uv.Mode.has('E'); badOtr {
				// Tried to send non-OTR message to another user.
				return nil
			}
			uv, err = decodeUserValue(stm.Get(keyCtl))
			if err != nil {
				return err
			}
			if badOtr = uv.Mode.has('E'); badOtr {
				// Tried to send non-OTR message to OTR'd user.
				return nil
			}
		}
		stm.Put(keyMsg, v, etcd.WithIgnoreLease())
		return nil
	}
	_, err := v3sync.NewSTM(
		ec.s.cli,
		f,
		v3sync.WithAbortContext(ec.ctx),
		v3sync.WithIsolation(v3sync.Serializable),
		v3sync.WithPrefetch(keyCtl, myUserCtl),
	)
	if err != nil {
		return err
	}
	if noNick {
		glog.V(9).Infof("%q PRIVMSG to %q not found", ec.nick, target)
		return ec.sendHostMsg(
			irc.ERR_NOSUCHNICK,
			ec.nick,
			target,
			"No such nick/channel")
	}
	if badOtr {
		glog.V(9).Infof("%q PRIVMSG to %q not OTR", ec.nick, target)
		return ec.sendHostMsg(
			irc.ERR_NOTEXTTOSEND,
			ec.nick,
			target,
			"No text to send")
	}
	return nil
}

func (ec *etcdClient) Nick(n string) error { panic("STUB") }

type replyList struct {
	channel string
	visible int
	topic   string
}

func (ec *etcdClient) List(chs []string) error {
	repls := []replyList{}

	// TODO fetch visible user counts instead of faking it

	if len(chs) == 0 {
		// fetch all channels
		resp, err := ec.s.cli.Get(ec.ctx, keyChanCtl(""), etcd.WithPrefix())
		if err != nil {
			return err
		}
		for _, kv := range resp.Kvs {
			chctl, err := decodeChannelCtl(string(kv.Value))
			if err != nil {
				return err
			}
			repls = append(repls, replyList{chctl.Name, 100, chctl.Topic})
		}
	} else {
		// fetch some channels
		for _, ch := range chs {
			resp, err := ec.s.cli.Get(ec.ctx, keyChanCtl(ch), etcd.WithPrefix())
			if err != nil {
				return err
			}
			if len(resp.Kvs) == 0 {
				continue
			}
			chctl, err := decodeChannelCtl(string(resp.Kvs[0].Value))
			if err != nil {
				return err
			}
			repls = append(repls, replyList{chctl.Name, 100, chctl.Topic})
		}
	}

	// do not send if no channels
	for _, repl := range repls {
		// "<channel> <# visible> :<topic>"
		ec.sendHostMsg(
			irc.RPL_LIST,
			ec.nick,
			repl.channel,
			fmt.Sprintf("%d", repl.visible),
			" "+repl.topic)
	}
	return ec.sendHostMsg(irc.RPL_LISTEND, "End of LIST")
}

func (ec *etcdClient) Names(ch string) error {
	// TODO support names for all channels and multi channel
	return ec.names(ch, 0)
}

func (ec *etcdClient) Part(ch, msg string) error {
	if !isChan(ch) {
		return ec.sendHostMsg(irc.ERR_NOSUCHCHANNEL, ch, "No such channel")
	}

	msgVal := encodeMessage(irc.Message{
		Prefix:  &ec.prefix,
		Command: irc.PART,
		Params:  []string{ch, msg},
	})
	// XXX use session for nick id
	ss, err := ec.s.ses.Session()
	if err != nil {
		return err
	}
	lid := int64(ss.Lease())
	userCtl, chNicks, chMsg := keyUserCtl(ec.nick), keyChanNicks(ch, lid), keyChanMsg(ch)
	onChannel := false
	f := func(stm v3sync.STM) error {
		onChannel = false

		// Remove channel from user.
		uv, err := decodeUserValue(stm.Get(userCtl))
		if err != nil {
			return err
		}
		for i, uvch := range uv.Channels {
			if uvch == ch {
				uv.Channels = append(uv.Channels[:i], uv.Channels[i+1:]...)
				onChannel = true
				break
			}
		}
		if !onChannel {
			return nil
		}
		stm.Put(userCtl, encodeUserValue(*uv), etcd.WithIgnoreLease())

		// Remove user from server's channel nick list.
		var cns ChannelNicks
		nicks := stm.Get(chNicks)
		if len(nicks) > 0 {
			cn, err := decodeChannelNicks(nicks)
			if err != nil {
				return err
			}
			cns = *cn
		}
		for i, cnick := range cns.Nicks {
			if cnick.Nick == ec.nick {
				cns.Nicks = append(cns.Nicks[:i], cns.Nicks[i+1:]...)
				break
			}
		}
		nicks = encodeChannelNicks(cns)
		stm.Put(chNicks, nicks, etcd.WithIgnoreLease())

		// Broadcast the part to the channel members.
		stm.Put(chMsg, msgVal)
		return nil
	}

	ctx, cancel := context.WithTimeout(ec.ctx, 5*time.Second)
	_, err = v3sync.NewSTM(
		ec.s.cli,
		f,
		v3sync.WithAbortContext(ctx),
		v3sync.WithIsolation(v3sync.Serializable),
		v3sync.WithPrefetch(userCtl, chNicks),
	)
	cancel()
	if !onChannel {
		return ec.sendHostMsg(irc.ERR_NOTONCHANNEL, ch, "You're not on that channel")
	}

	return err
}

func (ec *etcdClient) Whois(n string) error {
	if isChan(n) {
		glog.V(9).Infof("whois: no such nick %q", n)
		return ec.sendHostMsg(irc.ERR_NOSUCHNICK, n, "No such nick")
	}
	resp, err := ec.s.cli.Get(ec.ctx, keyUserCtl(n))
	if err != nil {
		glog.V(9).Infof("whois: failed to get key", n)
		return err
	}
	if len(resp.Kvs) == 0 || len(resp.Kvs[0].Value) == 0 {
		glog.V(9).Infof("whois: no such nick %q", n)
		return ec.sendHostMsg(irc.ERR_NOSUCHNICK, n, "No such nick")
	}
	uv, err := decodeUserValue(string(resp.Kvs[0].Value))
	if err != nil {
		glog.V(9).Infof("whois: failed to decode value")
		return err
	}
	// "<nick> <user> <host> * : <real name>"
	ec.sendHostMsg(irc.RPL_WHOISUSER, ec.nick, n, uv.User[1], "servername", "*", " norton")
	if len(uv.Channels) > 0 {
		// "<nick> :*( ( "@" / "+" ) <channel> " " )"
		ec.sendHostMsg(irc.RPL_WHOISCHANNELS, ec.nick, n, strings.Join(uv.Channels, " "))
	}
	// TODO idle time
	// TODO modes
	return ec.sendHostMsg(irc.RPL_ENDOFWHOIS, n)
}

func (ec *etcdClient) Close() error {
	ec.cancel()
	ec.wg.Wait()
	return nil
}

func (ec *etcdClient) Topic(ch, msg string) error {
	if !isChan(ch) {
		return ec.sendHostMsg(irc.ERR_NOSUCHCHANNEL, ch, "No such channel")
	}
	msgVal := encodeMessage(irc.Message{
		Prefix:  &ec.prefix,
		Command: irc.TOPIC,
		Params:  []string{ch, msg},
	})
	chCtl, chMsg := keyChanCtl(ch), keyChanMsg(ch)
	f := func(stm v3sync.STM) error {
		// Create channel if it doesn't exist and fetch topic.
		chctlv := stm.Get(chCtl)
		if len(chctlv) == 0 {
			// TODO: send no such channel
			return nil
		}
		chv, err := decodeChannelCtl(chctlv)
		if err != nil {
			return err
		}
		chv.Topic = msg
		stm.Put(chCtl, encodeChannelCtl(*chv))

		// Broadcast the topic update to the channel members.
		stm.Put(chMsg, msgVal)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	_, err := v3sync.NewSTM(
		ec.s.cli,
		f,
		v3sync.WithAbortContext(ctx),
		v3sync.WithIsolation(v3sync.Serializable),
		v3sync.WithPrefetch(chCtl),
	)
	cancel()
	return err
}
