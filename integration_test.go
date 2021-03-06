package etcdircd

import (
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"hash"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/integration"
	"github.com/coreos/etcd/pkg/testutil"
	"github.com/coreos/etcd/pkg/transport"
	"github.com/heyitsanthony/etcdcrypto"
	"gopkg.in/sorcix/irc.v2"

	"github.com/heyitsanthony/etcdircd/keyencode"
)

func TestUserMode(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()

	// Reject setting mode on another user.
	cn.Send(context.TODO(), irc.MODE, "badNick", "-i")
	testExpectMsg(t, cn, irc.ERR_USERSDONTMATCH)

	// Channel modes are not yet supported.
	cn.Send(context.TODO(), irc.MODE, "#somechan", "+i")
	testExpectMsg(t, cn, irc.ERR_NOCHANMODES)

	// No '+' or '-' is OK, but should still return errors.
	cn.Send(context.TODO(), irc.MODE, "n1", "Q")
	testExpectMsg(t, cn, "Q is unknown mode char to me")
	cn.Send(context.TODO(), irc.MODE, "n1", "+Q")
	testExpectMsg(t, cn, "Q is unknown mode char to me")

	cn.Send(context.TODO(), irc.MODE, "n1")
	testExpectMsg(t, cn, irc.RPL_UMODEIS)
}

func TestChanMode(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	var cn [3]*Conn
	for i := range cn {
		cn[i] = s.client(t, fmt.Sprintf("n%d", i))
		defer cn[i].Close()
	}

	// n0 is op
	cn[0].Send(context.TODO(), irc.JOIN, "#chan")
	testExpectMsg(t, cn[0], irc.RPL_ENDOFNAMES)
	// n1 is not op
	cn[1].Send(context.TODO(), irc.JOIN, "#chan")
	testExpectMsg(t, cn[1], irc.RPL_ENDOFNAMES)
	testExpectMsg(t, cn[0], irc.JOIN)

	// +s; hide channel from list
	// not op
	cn[1].Send(context.TODO(), irc.MODE, "#chan", "+s")
	testExpectMsg(t, cn[1], irc.ERR_CHANOPRIVSNEEDED)
	cn[1].Send(context.TODO(), irc.LIST)
	testExpectMsgs(t, cn[1], []string{"#chan", irc.RPL_LISTEND})
	// op
	cn[0].Send(context.TODO(), irc.MODE, "#chan", "+s")
	testExpectMsg(t, cn[0], "#chan")
	cn[2].Send(context.TODO(), irc.LIST)
	testExpectRejectMsgs(t, cn[2], []string{irc.RPL_LISTEND}, []string{"#chan"})
	// unset
	cn[0].Send(context.TODO(), irc.MODE, "#chan", "-s")
	testExpectMsg(t, cn[0], "#chan")
	cn[2].Send(context.TODO(), irc.LIST)
	testExpectMsgs(t, cn[2], []string{"#chan", irc.RPL_LISTEND})

	// TODO: hide from whois/names

	// +m; only op/voice can message channel
	cn[0].Send(context.TODO(), irc.MODE, "#chan", "+m")
	testExpectMsg(t, cn[0], irc.MODE)
	cn[1].Send(context.TODO(), irc.PRIVMSG, "#chan", "hello")
	testExpectMsg(t, cn[1], irc.ERR_CANNOTSENDTOCHAN)
	cn[0].Send(context.TODO(), irc.PRIVMSG, "#chan", "hello from cn0")
	testExpectMsg(t, cn[1], "hello from cn0")

	// +t; topic cannot be set by operators
	cn[0].Send(context.TODO(), irc.MODE, "#chan", "+t", "-m")
	testExpectMsg(t, cn[0], irc.MODE)
	cn[1].Send(context.TODO(), irc.TOPIC, "#chan", "new topic")
	testExpectMsg(t, cn[1], irc.ERR_CHANOPRIVSNEEDED)
	cn[0].Send(context.TODO(), irc.TOPIC, "#chan", "topic2")
	testExpectMsg(t, cn[0], "topic2")
	testExpectMsg(t, cn[1], "topic2")
	cn[1].Send(context.TODO(), irc.TOPIC, "#chan")
	testExpectMsg(t, cn[1], "topic2")
}

func TestKick(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	var cn [5]*Conn
	for i := range cn {
		cn[i] = s.client(t, fmt.Sprintf("n%d", i))
		defer cn[i].Close()
		cn[i].Send(context.TODO(), irc.JOIN, "#chan")
		testExpectMsg(t, cn[i], "#chan")
	}
	cn[4].Send(context.TODO(), irc.PART, "#chan")
	testExpectMsg(t, cn[4], irc.PART)

	// try to kick when not an op
	cn[1].Send(context.TODO(), irc.KICK, "#chan", "n2", "bye")
	testExpectMsg(t, cn[1], irc.ERR_CHANOPRIVSNEEDED)

	// try to kick a user that does not exist
	cn[0].Send(context.TODO(), irc.KICK, "#chan", "nn", "bye")
	testExpectMsg(t, cn[0], irc.ERR_USERNOTINCHANNEL)

	// try to kick a user that does exist in channel
	cn[0].Send(context.TODO(), irc.KICK, "#chan", "n4", "bye")
	testExpectMsg(t, cn[0], irc.ERR_USERNOTINCHANNEL)

	// try to kick multiple users
	cn[0].Send(context.TODO(), irc.KICK, "#chan", "n1,n2", "bye")
	testExpectMsg(t, cn[0], "bye")
	testExpectMsg(t, cn[0], "bye")

	// kicked user should receive kicked message
	testExpectMsg(t, cn[1], "bye")
	testExpectMsg(t, cn[2], "bye")
	testExpectMsg(t, cn[2], "bye")
	// non-kicked users should receive kicked message
	testExpectMsg(t, cn[3], "bye")
	testExpectMsg(t, cn[3], "bye")

	// kicked user should not receive new channel messages
	cn[0].Send(context.TODO(), irc.PRIVMSG, "#chan", "some message")
	testutil.AssertTrue(t, expectSilence(cn[1]))
	testutil.AssertTrue(t, expectSilence(cn[2]))
	// non-kicked user should receive new channel messages
	testExpectMsg(t, cn[3], "some message")
}

func TestOper(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn1 := s.client(t, "n1")
	defer cn1.Close()

	cn2 := s.client(t, "n2")
	defer cn2.Close()

	cn2.Send(context.TODO(), irc.MODE, "n2", "+i")
	testExpectMsg(t, cn2, irc.MODE)

	// Too few params.
	cn1.Send(context.TODO(), irc.OPER, "n1")
	testExpectMsg(t, cn1, irc.ERR_NEEDMOREPARAMS)

	// Try to become operator without credentials.
	cn1.Send(context.TODO(), irc.OPER, "guy", "pass")
	testExpectMsg(t, cn1, irc.ERR_PASSWDMISMATCH)

	// Reject Die command.
	cn1.Send(context.TODO(), irc.DIE)
	testExpectMsg(t, cn1, irc.ERR_NOPRIVILEGES)

	// Manually add a local operator.
	ov := OperValue{Pass: "pass", Global: false}
	_, err := s.s.cli.Put(context.TODO(), keyOperCtl("guy"), encodeOperValue(ov))
	testutil.AssertNil(t, err)

	cn1.Send(context.TODO(), irc.OPER, "guy", "pass")
	testExpectMsg(t, cn1, irc.RPL_YOUREOPER)

	// List invisible users.
	cn1.Send(context.TODO(), irc.WHO, "0")
	testExpectMsg(t, cn1, "n2")

	cn1.Send(context.TODO(), irc.MODE, "n1", "-o")
	testExpectMsg(t, cn1, irc.MODE)

	// Reject Die command.
	cn1.Send(context.TODO(), irc.DIE)
	testExpectMsg(t, cn1, irc.ERR_NOPRIVILEGES)

	// Manually add a global operator.
	ov = OperValue{Pass: "pass2", Global: true}
	_, err = s.s.cli.Put(context.TODO(), keyOperCtl("guy"), encodeOperValue(ov))
	testutil.AssertNil(t, err)

	cn1.Send(context.TODO(), irc.OPER, "guy", "pass2")
	testExpectMsg(t, cn1, irc.RPL_YOUREOPER)
	// List invisible users.
	cn1.Send(context.TODO(), irc.WHO, "0")
	testExpectMsg(t, cn1, "n2")

	cn1.Send(context.TODO(), irc.MODE, "n1", "-O")
	testExpectMsg(t, cn1, irc.MODE)

	// Reject Die command.
	cn1.Send(context.TODO(), irc.DIE)
	testExpectMsg(t, cn1, irc.ERR_NOPRIVILEGES)
}

func TestAway(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn1 := s.client(t, "n1")
	defer cn1.Close()
	cn2 := s.client(t, "n2")
	defer cn2.Close()

	// Don't allow setting away without AWAY command.
	cn2.Send(context.TODO(), irc.MODE, "n2", "+a")
	testExpectMsg(t, cn2, "a is unknown mode char to me")

	cn2.Send(context.TODO(), irc.AWAY, "I am away")
	testExpectMsg(t, cn2, irc.RPL_NOWAWAY)

	cn1.Send(context.TODO(), irc.PRIVMSG, "n2", "hello")
	testExpectMsg(t, cn1, "I am away")
	testutil.AssertTrue(t, expectSilence(cn2))

	cn2.Send(context.TODO(), irc.AWAY)
	testExpectMsg(t, cn2, irc.RPL_UNAWAY)

	cn1.Send(context.TODO(), irc.PRIVMSG, "n2", "hello")
	testExpectMsg(t, cn2, "hello")
}

func TestInvisible(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cns := make([]*Conn, 3)
	for i := range cns {
		cns[i] = s.client(t, fmt.Sprintf("n%d", i))
		defer cns[i].Close()
	}

	cns[0].Send(context.TODO(), irc.JOIN, "#chan")
	testExpectMsg(t, cns[0], "chan")
	cns[1].Send(context.TODO(), irc.JOIN, "#chan")
	testExpectMsg(t, cns[1], "chan")

	cns[1].Send(context.TODO(), irc.JOIN, "#hello")
	testExpectMsg(t, cns[1], "hello")
	cns[2].Send(context.TODO(), irc.JOIN, "#hello")
	testExpectMsg(t, cns[2], "hello")

	// match wildcard
	cns[0].Send(context.TODO(), irc.WHO, "0")
	testExpectMsgs(t, cns[0], []string{"n0", "n1", "n2"})

	// match channel
	cns[0].Send(context.TODO(), irc.WHO, "#chan")
	testExpectMsgs(t, cns[0], []string{"n0", "n1"})
	cns[0].Send(context.TODO(), irc.WHO, "#hello")
	testExpectMsgs(t, cns[0], []string{"n1", "n2"})

	// Mark n2 as invisible.
	cns[2].Send(context.TODO(), irc.MODE, "n2", "+i")
	testExpectMsg(t, cns[2], irc.MODE)

	// No channels shared between n0 and n2, so don't show n1 in wildcard.
	cns[0].Send(context.TODO(), irc.WHO, "*")
	testExpectRejectMsgs(t, cns[0], []string{"n0", "n1"}, []string{"n2"})

	// n1 and n2 share #hello, but not #chan.
	cns[1].Send(context.TODO(), irc.WHOIS, "n2")
	testExpectRejectMsgs(t, cns[1], []string{"#hello"}, []string{"#chan"})

	// Should see n1 but not n2 in #hello.
	cns[0].Send(context.TODO(), irc.NAMES, "#hello")
	testExpectRejectMsgs(t, cns[0], []string{"n1"}, []string{"n2"})

	// Mark n2 as visible.
	cns[2].Send(context.TODO(), irc.MODE, "n2", "-i")
	testExpectMsg(t, cns[2], irc.MODE)
	cns[0].Send(context.TODO(), irc.NAMES, "#hello")
	testExpectMsg(t, cns[0], "n2")
}

// TestModeOTR checks that the +E OTR-only mode is respected.
func TestModeOTR(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()

	cn2 := s.client(t, "n2")
	defer cn2.Close()

	myinfo := expectMsgVal(cn, irc.RPL_MYINFO)
	testutil.AssertTrue(t, myinfo != "")
	ss := strings.Split(myinfo, " ")
	// user modes
	testutil.AssertTrue(t, strings.Contains(ss[5], "E"))
	// channel modes
	testutil.AssertTrue(t, strings.Contains(ss[6], "E"))

	// Set user mode +E.
	cn.Send(context.TODO(), irc.MODE, "n1", "+E")
	testExpectMsg(t, cn, "E")

	// Try to send non-OTR message to plaintext user.
	cn.Send(context.TODO(), irc.PRIVMSG, "n2", "hello")
	testExpectMsg(t, cn, "No text to send")

	// Try to send non-OTR message to OTR user.
	cn2.Send(context.TODO(), irc.PRIVMSG, "n1", "hello")
	testExpectMsg(t, cn2, "No text to send")

	// Send OTR message to plaintext user.
	cn.Send(context.TODO(), irc.PRIVMSG, "n2", "?OTR")
	testExpectMsg(t, cn2, "?OTR")

	// Send OTR message from plaintext to OTR user.
	cn2.Send(context.TODO(), irc.PRIVMSG, "n1", "?OTR")
	testExpectMsg(t, cn, "?OTR")
}

// TestRejectDuplicateUsers checks a user will get a rejection
// message if the nick is already registered.
func TestRejectDuplicateUsers(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()

	c2, err := net.Dial("tcp", s.addr)
	testutil.AssertNil(t, err)
	cn2 := NewConn(c2)
	defer cn2.Close()

	// Reject nick already in use.
	cn2.Send(context.TODO(), "USER", "n1", "*", "*", "*")
	cn2.Send(context.TODO(), "NICK", "n1")
	testExpectMsg(t, cn2, "433 n1")

	// Try another nick.
	cn2.Send(context.TODO(), "NICK", "n2")
	testExpectMsg(t, cn2, "Welcome")
}

// TestJoin checks a user can join a channel and communicate.
func TestJoin(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()

	// Join channel with bogus name.
	cn.Send(context.TODO(), "JOIN", "badchan")
	testExpectMsg(t, cn, irc.ERR_NOSUCHCHANNEL)

	cn.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn, "JOIN")
	testExpectMsg(t, cn, "No topic")
	testExpectMsg(t, cn, "End of NAMES")

	// Join multiple channels at once.
	cn.Send(context.TODO(), "JOIN", "#chan1,#chan2")
	// Response may be interleaved, so can't check chan1/chan2 in order;
	// but the command should trigger exactly two NAMES responses.
	testExpectMsg(t, cn, "End of NAMES")
	testExpectMsg(t, cn, "End of NAMES")

	// List names in the channel #mychan.
	cn2 := s.client(t, "n2")
	defer cn2.Close()
	cn2.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn2, "JOIN")
	testExpectMsg(t, cn2, "n1")
	testExpectMsg(t, cn2, "End of NAMES")

	// First member in chat receives update that new user has joined.
	testExpectMsg(t, cn, "n2")

	// Bi-directional communication in the channel.
	cn.Send(context.TODO(), "PRIVMSG", "#mychan", "hello")
	testExpectMsg(t, cn2, "hello")
	cn2.Send(context.TODO(), "PRIVMSG", "#mychan", "greetz")
	testExpectMsg(t, cn, "greetz")

	// Receive part messages.
	cn.Send(context.TODO(), "PART", "#mychan", "goodbye")
	testExpectMsg(t, cn, "PART")
	testExpectMsg(t, cn2, "PART")

	// Receive quit messages.
	cn.Send(context.TODO(), "JOIN", "#mychan")
	cn.Send(context.TODO(), "QUIT", "byebye")
	testExpectMsg(t, cn2, "QUIT")
}

// TestPrivMsg tests a user can sent a private message to another user.
func TestPrivMsg(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()

	// Try to message someone not around
	cn.Send(context.TODO(), "PRIVMSG", "n2", "hello")
	testExpectMsg(t, cn, "No such")

	// Try to message someone logged in
	cn2 := s.client(t, "n2")
	defer cn2.Close()

	// Bi-directional communication
	cn.Send(context.TODO(), "PRIVMSG", "n2", "hello")
	testExpectMsg(t, cn2, "hello")
	cn2.Send(context.TODO(), "PRIVMSG", "n1", "greetz")
	testExpectMsg(t, cn, "greetz")

	// Try to message after QUIT
	cn2.Send(context.TODO(), "QUIT", "bye")
	testExpectMsg(t, cn2, "ERROR")

	cn.Send(context.TODO(), "PRIVMSG", "n2", "abc")
	testExpectMsg(t, cn, "No such")
}

// TestQuit checks that sending a quit properly removes the user.
func TestQuit(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()
	cn.Send(context.TODO(), "QUIT", "bye")
	testExpectMsg(t, cn, "ERROR")

	cn2 := s.client(t, "n1")
	defer cn2.Close()
	testExpectMsg(t, cn2, "Your host")
}

// TestTopic checks topics can be set and received.
func TestTopic(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()
	cn.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn, "JOIN")
	cn.Send(context.TODO(), irc.TOPIC, "#mychan", "some topic")
	testExpectMsg(t, cn, "some topic")

	cn2 := s.client(t, "n2")
	defer cn2.Close()
	cn2.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn2, "some topic")

	cn.Send(context.TODO(), irc.TOPIC, "#mychan", "topic2")
	testExpectMsg(t, cn, "topic2")
	testExpectMsg(t, cn2, "topic2")
}

// TestList checks that channels are being listed.
func TestList(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()
	cn.Send(context.TODO(), "JOIN", "#mychan")

	cn.Send(context.TODO(), "LIST")
	testExpectMsg(t, cn, "#mychan")

	cn.Send(context.TODO(), irc.TOPIC, "#mychan", "some topic")
	testExpectMsg(t, cn, "some topic")

	cn.Send(context.TODO(), "LIST")
	testExpectMsg(t, cn, "some topic")
}

// TestSessionRetryUser checks users will be evicted on lost session.
func TestSessionRetryUser(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()
	cn.Send(context.TODO(), "JOIN", "#mychan")

	// Destroy the session, taking n1 with it.
	ss, err := s.s.ses.Session()
	testutil.AssertNil(t, err)
	ss.Close()

	// Register a new user with a new session.
	cn2 := s.client(t, "n2")
	defer cn2.Close()

	// Session should be flushed, try to re-register user from old session.
	cn3 := s.client(t, "n1")
	defer cn3.Close()
}

// TestSessionPart checks a leave will be sent to other servers on lost session.
func TestSessionPart(t *testing.T) {
	clus := newIRCCluster(t)
	defer clus.Close()

	// Set up two IRC servers on the same etcd backend.
	testutil.AssertNil(t, clus.addIRC())
	testutil.AssertNil(t, clus.addIRC())
	s1, s2 := clus.irc[0], clus.irc[1]

	cn := s1.client(t, "n1")
	defer cn.Close()
	cn.Send(context.TODO(), "JOIN", "#mychan")

	cn2 := s2.client(t, "n2")
	defer cn2.Close()
	cn2.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn2, "n1")

	// Destroy first server's lease to trigger PART to be sent to n2.
	ss, err := s1.s.ses.Session()
	testutil.AssertNil(t, err)
	ss.Close()

	// Expect a part from n1 leaving.
	testExpectMsg(t, cn2, "PART")
}

// TestPartNoMsg checks that parting with no message works.
func TestPartNoMsg(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	cn := s.client(t, "n1")
	defer cn.Close()
	cn.Send(context.TODO(), "JOIN", "#mychan")

	cn.Send(context.TODO(), "PART", "#mychan")
	testExpectMsg(t, cn, "#mychan")

	cn.Send(context.TODO(), "PART", "#mychan")
	testExpectMsg(t, cn, "You're not on that channel")
}

// TestPart checks users do not receive channel messages after parting.
func TestPart(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()

	clientTotal := 32

	var wg sync.WaitGroup
	wg.Add(clientTotal)
	cs := make([]*Conn, clientTotal)
	ctx, cancel := context.WithCancel(context.TODO())
	for i := range cs {
		go func(v int) {
			defer wg.Done()
			n := fmt.Sprintf("n%d", v)
			c := s.client(t, n)
			cs[v] = c
			for ctx.Err() != nil {
				if err := c.Send(ctx, "JOIN", "#mychan"); err != nil {
					return
				}
				testExpectMsg(t, c, n)
				if err := c.Send(ctx, "PART", "#mychan", "bye"); err != nil {
					return
				}
				testExpectMsg(t, c, n)
				testutil.AssertTrue(t, expectSilence(c))
			}
		}(i)
	}

	time.Sleep(3 * time.Second)
	cancel()
	wg.Wait()
}

var plaintextChecks = []string{
	// topic name
	"some topic",
	// channel name
	"mychan",
	// user name
	"secretuser",
	// private message
	"secretmsg",
	// channel message
	"chanmsg",
}

func testPlaintext(t *testing.T, s *ircServer) map[string]string {
	cn := s.client(t, "secretuser")
	defer cn.Close()

	cn.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn, "mychan")
	cn.Send(context.TODO(), irc.TOPIC, "#mychan", "some topic")

	cn2 := s.client(t, "secretuser2")
	defer cn2.Close()

	cn2.Send(context.TODO(), "PRIVMSG", "secretuser", "secretmsg")
	testExpectMsg(t, cn, "secretmsg")

	cn2.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn2, "some topic")
	cn.Send(context.TODO(), "PRIVMSG", "#mychan", "chanmsg")
	testExpectMsg(t, cn2, "chanmsg")

	// Fetch complete etcd data.
	resp, err := s.etcd.Client(0).Get(context.TODO(), "", etcd.WithPrefix())
	testutil.AssertNil(t, err)

	leaks := make(map[string]string)
	for _, kv := range resp.Kvs {
		k, v := string(kv.Key), string(kv.Value)
		for _, check := range plaintextChecks {
			if strings.Contains(v, check) || strings.Contains(k, check) {
				leaks[check] = k
			}
		}
	}
	return leaks
}

// TestPlaintext checks that unencrypted data shows plaintext. This is important
// for the encryption test, which checks if that plaintext still exists.
func TestPlaintext(t *testing.T) {
	s := newIRCServer(t)
	defer s.Close()
	if leaks := testPlaintext(t, s); len(leaks) != len(plaintextChecks) {
		t.Errorf("got %v leaks, expected %v", leaks, plaintextChecks)
	}
}

// TestEncryptedNoPlaintext checks that strings from sessions aren't
// appearing as plaintext in etcd.
func TestEncryptedNoPlaintext(t *testing.T) {
	if _, err := os.Stat("hack/tls/certs/etcdcrypto/server1.pem"); err != nil {
		t.Skip("make hack/tls certs to test")
	}
	clus := newIRCCluster(t)
	defer clus.Close()

	cli := clus.etcd.Client(0)
	scli := etcd.NewCtxClient(cli.Ctx())
	scli.KV = cli.KV
	scli.Watcher = cli.Watcher
	scli.Lease = cli.Lease

	// Copied from cmd/etcdircd/main.go. Need a separate crypto package for this?

	// Negotiate AES key.
	tlsinfo := transport.TLSInfo{
		KeyFile:  "hack/tls/certs/etcdcrypto/server1-key.pem",
		CertFile: "hack/tls/certs/etcdcrypto/server1.pem",
		CAFile:   "hack/tls/certs/etcdcrypto/ca.pem",
	}
	kx, kxerr := etcdcrypto.NewKeyExchange(cli, "", tlsinfo)
	testutil.AssertNil(t, kxerr)
	var aeskey []byte
	select {
	case aeskey = <-kx.SymmetricKey():
	case <-time.After(30 * time.Second):
		t.Fatal("could not establish session key")
	}
	cipher, cerr := etcdcrypto.NewAESCipher(aeskey)
	testutil.AssertNil(t, cerr)

	// Set up key encryption.
	h := func() hash.Hash { return hmac.New(sha256.New, aeskey) }
	b, err := aes.NewCipher(aeskey)
	testutil.AssertNil(t, err)
	encoder := NewKeyEncoder(keyencode.NewKeyEncoderHMAC(h, b))

	// Encrypt keys and values.
	encKv := etcdcrypto.NewKV(scli.KV, cipher)
	encWatcher := etcdcrypto.NewWatcher(scli.Watcher, cipher)
	scli.KV = keyencode.NewKV(encKv, encoder)
	scli.Watcher = keyencode.NewWatcher(encWatcher, encoder)

	clus.addIRCWithClient(scli)

	if leaks := testPlaintext(t, clus.irc[0]); len(leaks) > 0 {
		t.Errorf("got leaks %v", leaks)
	}
}

type ircCluster struct {
	etcd   *integration.ClusterV3
	irc    []*ircServer
	cancel func()
}

func newIRCCluster(t *testing.T) *ircCluster {
	etcd := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 1})
	return &ircCluster{etcd: etcd, cancel: func() { etcd.Terminate(t) }}
}

func (clus *ircCluster) addIRC() error {
	return clus.addIRCWithClient(clus.etcd.RandClient())
}

func (clus *ircCluster) addIRCWithClient(cli *etcd.Client) error {
	port := 30000 + len(clus.irc)
	// TODO use unix socket
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	serv := NewServer(NewConfig(), ln, cli)
	donec := make(chan struct{})
	go func() {
		defer close(donec)
		serv.Serve()
	}()
	is := &ircServer{
		addr:   fmt.Sprintf("127.0.0.1:%d", port),
		etcd:   clus.etcd,
		s:      serv,
		cancel: func() { ln.Close() },
		donec:  donec,
	}
	clus.irc = append(clus.irc, is)
	return nil
}

func (clus *ircCluster) Close() {
	clus.cancel()
	for _, c := range clus.irc {
		c.Close()
	}
}

type ircServer struct {
	etcd *integration.ClusterV3

	s      *Server
	addr   string
	cancel func()
	donec  chan struct{}
}

func (s *ircServer) Close() {
	s.cancel()
	<-s.donec
}

func (s *ircServer) client(t *testing.T, nick string) *Conn {
	c, err := net.Dial("tcp", s.addr)
	testutil.AssertNil(t, err)
	cn := NewConn(c)
	cn.Send(context.TODO(), "USER", nick, "*", "*", "*")
	cn.Send(context.TODO(), "NICK", nick)
	if !expectMsg(cn, "Welcome") {
		cn.Close()
		t.Fatalf("could not log in %q", nick)
	}
	return cn
}

func newIRCServer(t *testing.T) *ircServer {
	clus := newIRCCluster(t)
	testutil.AssertNil(t, clus.addIRC())
	c := clus.irc[0].cancel
	clus.irc[0].cancel = func() {
		clus.etcd.Terminate(t)
		c()
	}
	return clus.irc[0]
}

func testExpectMsg(t *testing.T, cn *Conn, s string) {
	testutil.AssertTrue(t, expectMsg(cn, s))
}

func testExpectMsgs(t *testing.T, cn *Conn, s []string) {
	testutil.AssertTrue(t, len(expectMsgVals(cn, s, nil)) == len(s))
}

func testExpectRejectMsgs(t *testing.T, cn *Conn, s []string, rej []string) {
	testutil.AssertTrue(t, len(expectMsgVals(cn, s, rej)) == len(s))
}

func expectMsg(c *Conn, s string) bool { return expectMsgVal(c, s) != "" }

func expectMsgVal(c *Conn, s string) string {
	if ret := expectMsgVals(c, []string{s}, nil); len(ret) > 0 {
		return ret[0]
	}
	return ""
}

func expectMsgVals(c *Conn, s []string, rej []string) (ret []string) {
	unmatched := make(map[string]struct{}, len(s))
	for i := range s {
		unmatched[s[i]] = struct{}{}
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case msg := <-c.Reader():
			m := fmt.Sprintf("%v", msg)
			for _, r := range rej {
				if strings.Contains(m, r) {
					return nil
				}
			}
			for u := range unmatched {
				if strings.Contains(m, u) {
					ret = append(ret, m)
					delete(unmatched, u)
					break
				}
			}
			if len(unmatched) == 0 {
				return ret
			}
		case <-t.C:
			return nil
		}
	}
}

func expectSilence(c *Conn) bool {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case msg := <-c.Reader():
			if msg.Command == irc.PING || msg.Command == irc.PONG {
				continue
			}
			return false
		case <-t.C:
			return true
		}
	}
}
