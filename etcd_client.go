package etcdircd

import (
	"fmt"

	"context"
	"os"
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
	uv := UserValue{
		Nick:    cr.nick,
		User:    cr.user,
		Mode:    newModeValue(s.cfg.PinnedUserModes),
		Created: time.Now(),
	}

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
	if !resp.Succeeded {
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

// monitorPart waits for deletions on a channel's users prefix and reports the lost
// users as leaves to the other parties in the channel.
func (ec *etcdClient) monitorPart(ch string, usersc etcd.WatchChan) error {
	for resp := range usersc {
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
			if err := ec.sendParts(ch, *cns, ev.Kv.ModRevision-1); err != nil {
				return err
			}
		}
	}
	return ec.ctx.Err()
}

func (ec *etcdClient) sendParts(ch string, cns ChannelNicks, rev int64) error {
	glog.V(9).Infof("sendLeaves: %q sending %+v", ch, cns)
	users, err := ec.usersFromNicks(cns.Nicks, rev)
	if err != nil {
		return err
	}
	for _, u := range users {
		msg := irc.Message{
			Prefix:  &irc.Prefix{Name: u.Nick, User: u.User[1], Host: u.Host},
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
	return ec.SendMsg(
		ec.ctx,
		irc.Message{
			Prefix:  &ec.s.hostPfx,
			Command: cmd,
			Params:  append([]string{ec.nick}, params...),
		})
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
	ss, serr := ec.s.ses.Session()
	if serr != nil {
		return serr
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
			chvv, cerr := decodeChannelCtl(chctlv)
			if cerr != nil {
				return cerr
			}
			chv = *chvv
		}
		topic = chv.Topic
		stm.Put(chCtl, encodeChannelCtl(chv))

		// Add user to server's channel nick list.
		var cns ChannelNicks
		users := stm.Get(chNicks)
		if len(users) > 0 {
			cn, err := decodeChannelNicks(users)
			if err != nil {
				return err
			}
			cns = *cn
		}
		cns.Nicks = append(cns.Nicks, uv.Nick)
		stm.Put(chNicks, encodeChannelNicks(cns), etcd.WithLease(lid))

		// Broadcast the join to the channel members.
		stm.Put(chMsg, msgVal)
		return nil
	}
	rev, err := ec.doSTM(f, userCtl, chCtl, chNicks)
	if err != nil {
		return err
	}

	// TODO: if only nick, make channel operator

	// JOIN is successful; the user receives a JOIN message as confirmation.
	ec.SendMsg(ec.ctx, msg)
	// then sent the channel's topic (using RPL_TOPIC)
	if topic == "" {
		ec.sendHostMsg(irc.RPL_NOTOPIC, ch, "No topic is set")
	} else {
		ec.sendHostMsg(irc.RPL_TOPIC, ch, topic)
	}
	// and the list of users who are on the channel (using RPL_NAMREPLY)
	// Fetch users at time of join.
	if err := ec.names(ch, rev); err != nil {
		return err
	}
	ec.monitorChannel(ch, rev+1)
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
	users, err := ec.usersFromMask(ch, rev)
	if err != nil {
		return err
	}
	nicks := make([]string, len(users))
	for i, u := range users {
		nicks[i] = u.Nick
	}
	ec.sendHostMsg(irc.RPL_NAMREPLY, "=", ch, strings.Join(nicks, " "))
	return ec.sendHostMsg(irc.RPL_ENDOFNAMES, ch, "End of NAMES list")
}

func (ec *etcdClient) Who(args []string) error {
	// The <mask> passed to WHO is matched against users' host, server, real
	// name and nickname if the channel <mask> cannot be found.
	mask := ""
	if len(args) == 0 || args[0] == "0" {
		mask = "*"
	} else {
		mask = args[0]
	}
	users, err := ec.usersFromMask(mask, 0)
	if err != nil {
		return err
	}
	for _, u := range users {
		v := fmt.Sprintf("%s %s %s %s %s H",
			"*",
			u.User[1],
			u.Host,
			u.ServerName,
			u.Nick)
		if err := ec.sendHostMsg(irc.RPL_WHOREPLY, v, "0 "+u.RealName); err != nil {
			return err
		}
	}
	return ec.sendHostMsg(irc.RPL_ENDOFWHO, mask, "End of WHO list")
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
			// Remove from users in channel.
			chNicks := keyChanNicks(ch, int64(lid))
			users := stm.Get(chNicks)
			if len(users) == 0 {
				continue
			}
			cns, err := decodeChannelNicks(users)
			if err != nil {
				return err
			}
			cns.del(ec.nick)
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
	if _, err := ec.doSTM(f, userCtl); err != nil {
		glog.Warning(err)
		return err
	}
	return nil
}

func (ec *etcdClient) Mode(m []string) error {
	if isChan(m[0]) {
		return ec.sendHostMsg(irc.ERR_NOCHANMODES, m[0], "Channel doesn't support modes")
	}
	if m[0] != ec.nick {
		return ec.sendHostMsg(irc.ERR_USERSDONTMATCH, "Cannot change mode for other users")
	}

	mode := ""
	bad := []byte{}

	userCtl := keyUserCtl(ec.nick)
	f := func(stm v3sync.STM) error {
		uv, err := decodeUserValue(stm.Get(userCtl))
		if err != nil {
			return err
		}
		oldMode := uv.Mode
		for _, modeStr := range m[1:] {
			newMv, newBad := uv.Mode.update(modeStr)
			uv.Mode = newMv
			bad = append(bad, newBad...)
		}
		uv.Mode, _ = uv.Mode.update("+" + ec.s.cfg.PinnedUserModes)
		mode = string(uv.Mode)
		if uv.Mode.Equal(oldMode) {
			return nil
		}

		stm.Put(userCtl, encodeUserValue(*uv), etcd.WithIgnoreLease())
		return nil
	}
	if _, err := ec.doSTM(f, userCtl); err != nil {
		return err
	}
	if len(m) == 1 {
		return ec.sendHostMsg(irc.RPL_UMODEIS, mode)
	}
	if err := ec.sendHostMsg(irc.MODE, mode); err != nil {
		return err
	}
	for _, m := range bad {
		err := ec.sendHostMsg(irc.ERR_UMODEUNKNOWNFLAG, string(m)+" is unknown mode char to me")
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
	return ec.SendMsg(
		ec.ctx,
		irc.Message{
			Prefix:  &ec.s.hostPfx,
			Command: irc.PONG,
			Params:  []string{ec.s.cfg.HostName, msg},
		})
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
		uvTarget, terr := decodeUserValue(stm.Get(keyCtl))
		if !isChan(target) {
			if terr != nil {
				return terr
			}
			if uvTarget.Mode.has('a') {
				awayMsg := encodeMessage(irc.Message{
					Prefix:  &ec.prefix,
					Command: irc.RPL_AWAY,
					Params:  []string{ec.nick, target, uvTarget.AwayMsg},
				})
				stm.Put(keyUserMsg(ec.nick), awayMsg, etcd.WithIgnoreLease())
				return nil
			}
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
			if badOtr = uvTarget.Mode.has('E'); badOtr {
				// Tried to send non-OTR message to OTR'd user.
				return nil
			}
		}
		stm.Put(keyMsg, v, etcd.WithIgnoreLease())
		return nil
	}
	if _, err := ec.doSTM(f, keyCtl, myUserCtl); err != nil {
		return err
	}
	if noNick {
		glog.V(9).Infof("%q PRIVMSG to %q not found", ec.nick, target)
		return ec.sendHostMsg(
			irc.ERR_NOSUCHNICK,
			target,
			"No such nick/channel")
	}
	if badOtr {
		glog.V(9).Infof("%q PRIVMSG to %q not OTR", ec.nick, target)
		return ec.sendHostMsg(
			irc.ERR_NOTEXTTOSEND,
			target,
			"No text to send")
	}
	return nil
}

func (ec *etcdClient) Oper(login, pass string) error {
	loggedIn := false
	userKey, operKey := keyUserCtl(ec.nick), keyOperCtl(login)
	f := func(stm v3sync.STM) error {
		uv, err := decodeUserValue(stm.Get(userKey))
		if err != nil {
			return err
		}
		ovValue := stm.Get(operKey)
		if len(ovValue) == 0 {
			return nil
		}
		ov, err := decodeOperValue(ovValue)
		if err != nil {
			return err
		}
		if ov.Pass != pass {
			return nil
		}
		loggedIn = true
		if ov.Global {
			uv.Mode = uv.Mode.add('O')
		} else {
			uv.Mode = uv.Mode.add('o')
		}
		stm.Put(userKey, encodeUserValue(*uv), etcd.WithIgnoreLease())
		return nil
	}
	if _, err := ec.doSTM(f, userKey, operKey); err != nil {
		return err
	}
	if !loggedIn {
		return ec.sendHostMsg(irc.ERR_PASSWDMISMATCH, "Password incorrect")
	}
	return ec.sendHostMsg(irc.RPL_YOUREOPER, "You are now an IRC operator")
}

func (ec *etcdClient) Die() error {
	resp, err := ec.s.cli.Get(ec.ctx, keyUserCtl(ec.nick))
	if err != nil {
		return err
	}
	uv, err := decodeUserValue(string(resp.Kvs[0].Value))
	if err != nil {
		return err
	}
	if !uv.Mode.has('o') && !uv.Mode.has('O') {
		return ec.sendHostMsg(irc.ERR_NOPRIVILEGES, "Permission Denied- You're not an IRC operator")
	}
	// TODO: send die message to users, send die message to non-local servers
	os.Exit(0)
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
	msgVal := encodeMessage(irc.Message{
		Prefix:  &ec.prefix,
		Command: irc.PART,
		Params:  []string{ch, msg},
	})
	// XXX use session for nick id
	ss, serr := ec.s.ses.Session()
	if serr != nil {
		return serr
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
		if users := stm.Get(chNicks); len(users) > 0 {
			cn, err := decodeChannelNicks(users)
			if err != nil {
				return err
			}
			cn.del(ec.nick)
			stm.Put(chNicks, encodeChannelNicks(*cn), etcd.WithIgnoreLease())
		}

		// Broadcast the part to the channel members.
		stm.Put(chMsg, msgVal)
		return nil
	}
	if _, err := ec.doSTM(f, userCtl, chNicks); err != nil {
		return nil
	}
	if !onChannel {
		return ec.sendHostMsg(irc.ERR_NOTONCHANNEL, ch, "You're not on that channel")
	}
	return nil
}

func (ec *etcdClient) Whois(n string) error {
	if n == "" {
		n = ec.nick
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
	ec.sendHostMsg(irc.RPL_WHOISUSER, n, uv.User[1], "servername", "*", " norton")
	if len(uv.Channels) > 0 {
		// "<nick> :*( ( "@" / "+" ) <channel> " " )"
		ec.sendHostMsg(irc.RPL_WHOISCHANNELS, n, strings.Join(uv.Channels, " "))
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
	_, err := ec.doSTM(f, chCtl)
	return err
}

func (ec *etcdClient) Away(msg string) error {
	k := keyUserCtl(ec.nick)
	f := func(stm v3sync.STM) error {
		uv, err := decodeUserValue(stm.Get(k))
		if err != nil {
			return err
		}
		if msg == "" {
			uv.Mode = uv.Mode.del('a')
		} else {
			uv.Mode = uv.Mode.add('a')
		}
		uv.AwayMsg = msg
		stm.Put(k, encodeUserValue(*uv), etcd.WithIgnoreLease())
		return nil
	}
	if _, err := ec.doSTM(f, k); err != nil {
		return err
	}
	if msg == "" {
		return ec.sendHostMsg(irc.RPL_UNAWAY, "You are no longer marked as being away")
	}
	return ec.sendHostMsg(irc.RPL_NOWAWAY, "You have been marked as being away")
}

func (ec *etcdClient) usersFromMask(mask string, rev int64) ([]UserValue, error) {
	if isChan(mask) {
		return ec.usersFromChannel(mask, rev)
	}
	if mask == "*" {
		return ec.users(rev)
	}
	return ec.usersFromNicks([]string{mask}, rev)
}

func (ec *etcdClient) users(rev int64) ([]UserValue, error) {
	resp, err := ec.s.cli.Txn(ec.ctx).Then(
		etcd.OpGet(keyUserCtl(""), etcd.WithPrefix(), etcd.WithRev(rev)),
		etcd.OpGet(keyUserCtl(ec.nick), etcd.WithRev(rev)),
	).Commit()
	if err != nil {
		return nil, err
	}
	meKv := resp.Responses[1].GetResponseRange().Kvs[0]
	me, err := decodeUserValue(string(meKv.Value))
	if err != nil {
		return nil, err
	}
	users := []UserValue{}
	for _, kv := range resp.Responses[0].GetResponseRange().Kvs {
		u, err := decodeUserValue(string(kv.Value))
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return pruneInvisible(users, *me), nil
}

func (ec *etcdClient) usersFromChannel(ch string, rev int64) ([]UserValue, error) {
	resp, err := ec.s.cli.Get(ec.ctx,
		keyChanNicksPfx(ch),
		etcd.WithRev(rev),
		etcd.WithPrefix())
	if err != nil {
		return nil, err
	}
	nicks := []string{}
	for _, kv := range resp.Kvs {
		n, err := decodeChannelNicks(string(kv.Value))
		if err != nil {
			return nil, err
		}
		nicks = append(nicks, n.Nicks...)
	}
	return ec.usersFromNicks(nicks, resp.Header.Revision)
}

func (ec *etcdClient) usersFromNicks(nicks []string, rev int64) ([]UserValue, error) {
	ops := make([]etcd.Op, len(nicks)+1)
	for i, n := range nicks {
		ops[i] = etcd.OpGet(keyUserCtl(n), etcd.WithRev(rev))
	}
	ops[len(ops)-1] = etcd.OpGet(keyUserCtl(ec.nick), etcd.WithRev(rev))

	resp, err := ec.s.cli.Txn(ec.ctx).Then(ops...).Commit()
	if err != nil {
		return nil, err
	}
	users := []UserValue{}
	for _, r := range resp.Responses {
		uv, err := decodeUserValue(string(r.GetResponseRange().Kvs[0].Value))
		if err != nil {
			return nil, err
		}
		users = append(users, *uv)
	}
	return pruneInvisible(users[:len(users)-1], users[len(users)-1]), nil
}

func (ec *etcdClient) doSTM(f func(v3sync.STM) error, prefetch ...string) (int64, error) {
	resp, err := v3sync.NewSTM(
		ec.s.cli,
		f,
		v3sync.WithAbortContext(ec.ctx),
		v3sync.WithIsolation(v3sync.Serializable),
		v3sync.WithPrefetch(prefetch...),
	)
	if err != nil {
		return 0, err
	}
	return resp.Header.Revision, nil
}
