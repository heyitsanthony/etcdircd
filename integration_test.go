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

	// List names in the channel.
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
	cn.Send(context.TODO(), "TOPIC", "#mychan", "some topic")
	testExpectMsg(t, cn, "some topic")

	cn2 := s.client(t, "n2")
	defer cn2.Close()
	cn2.Send(context.TODO(), "JOIN", "#mychan")
	testExpectMsg(t, cn2, "some topic")

	cn.Send(context.TODO(), "TOPIC", "#mychan", "topic2")
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

	cn.Send(context.TODO(), "TOPIC", "#mychan", "some topic")
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

func testExpectMsg(t *testing.T, cn *Conn, s string) {
	testutil.AssertTrue(t, expectMsg(cn, s))
}

func expectMsg(c *Conn, s string) bool {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case msg := <-c.Reader():
			if strings.Contains(fmt.Sprintf("%+v", msg), s) {
				return true
			}
		case <-t.C:
			return false
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
	cn.Send(context.TODO(), "TOPIC", "#mychan", "some topic")

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
