package etcdcrypto

import (
	"fmt"

	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"io/ioutil"
	"sync"

	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/clientv3/concurrency"
	"github.com/coreos/etcd/mvcc/mvccpb"
	"github.com/coreos/etcd/pkg/tlsutil"
	"github.com/coreos/etcd/pkg/transport"

	"golang.org/x/net/context"
)

// KeyExchange publishes a public key to a prefix and
// the leader sends the system AES key encrypted by
// the public key.
type KeyExchange struct {
	cli *clientv3.Client
	pfx string
	tls transport.TLSInfo

	keyc  chan []byte
	donec chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	pubRev    int64  // revision of public key write
	certBytes string // raw data of crt file
	certSHA   string // hash of raw data
	x509cert  *x509.Certificate
	privKey   *rsa.PrivateKey

	certpool  *x509.CertPool
	sharedKey []byte
}

func NewKeyExchange(cli *clientv3.Client, pfx string, tls transport.TLSInfo) (*KeyExchange, error) {
	cctx, cancel := context.WithCancel(cli.Ctx())
	kx := &KeyExchange{
		cli: cli,
		pfx: pfx,
		tls: tls,

		keyc:   make(chan []byte, 1),
		donec:  make(chan struct{}),
		ctx:    cctx,
		cancel: cancel,
	}
	// load authorities
	cp, err := tlsutil.NewCertPool([]string{tls.CAFile})
	if err != nil {
		return nil, err
	}
	kx.certpool = cp
	// load cert
	cbytes, rerr := ioutil.ReadFile(tls.CertFile)
	if rerr != nil {
		return nil, rerr
	}
	kx.certBytes, kx.certSHA = string(cbytes), string(shaBytes(cbytes))
	block, _ := pem.Decode(cbytes)
	cert, cerr := x509.ParseCertificate(block.Bytes)
	if cerr != nil {
		return nil, cerr
	}
	kx.x509cert = cert
	// load priv key
	priv, perr := ioutil.ReadFile(tls.KeyFile)
	if err != nil {
		return nil, perr
	}
	block, _ = pem.Decode(priv)
	kx.privKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	// boot exchange
	go func() {
		defer close(kx.donec)
		for kx.ctx.Err() == nil {
			if err := kx.exchange(); err != nil {
				fmt.Println(err)
			}
		}
	}()
	return kx, nil
}

func (kx *KeyExchange) Close() {
	kx.cancel()
	<-kx.donec
}

func (kx *KeyExchange) SymmetricKey() <-chan []byte { return kx.keyc }

func (kx *KeyExchange) IsRegistered() clientv3.Cmp {
	pubpath := kx.pfx + "/cert/" + kx.certSHA
	return clientv3.Compare(clientv3.CreateRevision(pubpath), "=", kx.pubRev)
}

func (kx *KeyExchange) exchange() error {
	s, err := concurrency.NewSession(kx.cli, concurrency.WithTTL(5))
	if err != nil {
		return err
	}
	defer s.Close()
	// fetch / publish cert
	pubpath := kx.pfx + "/cert/" + kx.certSHA
	txresp, terr := kx.cli.Txn(kx.ctx).If(
		clientv3.Compare(clientv3.ModRevision(pubpath), "=", 0),
	).Then(
		clientv3.OpPut(pubpath, kx.certBytes, clientv3.WithLease(s.Lease())),
	).Else(
		clientv3.OpGet(pubpath),
	).Commit()
	if terr != nil {
		return terr
	}
	if !txresp.Succeeded {
		kv := txresp.Responses[0].GetResponseRange().Kvs[0]
		if kv.Version == 2 {
			if kx.decryptSessionKey(kv.Value) == nil {
				kx.pubRev = kv.CreateRevision
			}
		}
	}
	// TODO: use distributed mutex
	e := concurrency.NewElection(s, kx.pfx+"/lock")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for kx.ctx.Err() == nil {
			err = kx.campaign(e)
		}
	}()
	go func() {
		defer wg.Done()
		kx.observe()
	}()
	wg.Wait()
	return err
}

func (kx *KeyExchange) campaign(e *concurrency.Election) error {
	if err := e.Campaign(kx.ctx, kx.certBytes); err != nil {
		return err
	}
	// load in shared key or create new one
	if kx.sharedKey == nil {
		lresp, lerr := kx.cli.Get(kx.ctx, kx.pfx+"/cert/"+kx.certSHA)
		if lerr != nil {
			return lerr
		}
		switch {
		case len(lresp.Kvs) == 1 && lresp.Kvs[0].Version == 2:
			if kx.decryptSessionKey(lresp.Kvs[0].Value) == nil {
				kx.pubRev = lresp.Kvs[0].CreateRevision
			}
		case len(lresp.Kvs) == 1 && lresp.Kvs[0].Version == 1:
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return err
			}
			_, terr := kx.cli.Txn(kx.ctx).If(
				clientv3.Compare(clientv3.CreateRevision(e.Key()), "=", e.Rev()),
			).Then(
				clientv3.OpDelete(kx.pfx+"/sess/", clientv3.WithPrefix()),
			).Commit()
			if terr != nil {
				return terr
			}

			kx.sharedKey = key
			kx.keyc <- key
		}
	}
	// fetch current keys
	gresp, gerr := kx.cli.Get(kx.ctx, kx.pfx+"/cert/", clientv3.WithPrefix())
	if gerr != nil {
		return gerr
	}
	// update current keys if never received encrypted key
	for _, kv := range gresp.Kvs {
		if err := kx.encryptSessionKey(e, kv); err != nil {
			return err
		}
	}
	// watch for new public keys and write out encrypted key
	opts := []clientv3.OpOption{
		clientv3.WithRev(gresp.Header.Revision + 1),
		clientv3.WithPrefix(),
	}
	wch := kx.cli.Watch(kx.ctx, kx.pfx+"/cert/", opts...)
	for wresp := range wch {
		if wresp.Err() != nil {
			return wresp.Err()
		}
		for _, ev := range wresp.Events {
			if ev.Type != clientv3.EventTypePut {
				continue
			}
			if err := kx.encryptSessionKey(e, ev.Kv); err != nil {
				return err
			}
		}
	}

	return nil
}

func (kx *KeyExchange) observe() error {
	for kx.ctx.Err() == nil {
		wctx, wcancel := context.WithCancel(kx.ctx)
		wch := kx.cli.Watch(wctx, kx.pfx+"/cert/"+kx.certSHA)
		for wr := range wch {
			if len(wr.Events) == 0 {
				continue
			}
			if ev := wr.Events[len(wr.Events)-1]; ev.Kv.Version == 2 {
				if err := kx.decryptSessionKey(ev.Kv.Value); err != nil {
					panic(err)
				}
				kx.pubRev = ev.Kv.CreateRevision
			}
		}
		wcancel()
	}
	return kx.ctx.Err()
}

func (kx *KeyExchange) encryptSessionKey(e *concurrency.Election, kv *mvccpb.KeyValue) error {
	if kv.Version > 1 {
		// already written once
		return nil
	}
	// parse given cert
	block, _ := pem.Decode(kv.Value)
	if block == nil {
		return fmt.Errorf("could not decode PEM")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	vopts := x509.VerifyOptions{Roots: kx.certpool}
	if _, verr := c.Verify(vopts); verr != nil {
		// move requests with bad certs
		kx.cli.Txn(kx.ctx).If(
			clientv3.Compare(clientv3.ModRevision(string(kv.Key)), "=", kv.ModRevision),
		).Then(
			clientv3.OpDelete(string(kv.Key)),
		).Commit()
		return verr
	}
	// must use rsa since no way elliptic encrypt in go stdlib yet
	if c.PublicKeyAlgorithm != x509.RSA {
		return fmt.Errorf("expected public key algorithm %q, got %q", x509.RSA, c.PublicKeyAlgorithm)
	}
	pub, ok := c.PublicKey.(*rsa.PublicKey)
	if !ok {
		panic("oops bad type")
	}
	// encrypt shared key with requester's cert
	enc, eerr := rsa.EncryptOAEP(sha512.New512_256(), rand.Reader, pub, kx.sharedKey, nil)
	if eerr != nil {
		return eerr
	}
	// sign result with own cert
	sig, serr := rsa.SignPSS(rand.Reader, kx.privKey, crypto.SHA512_256, shaBytes(enc), nil)
	if serr != nil {
		return serr
	}
	// paste together mycert|enc|mysig
	// TODO: use gob / better format
	out := make([]byte, 8+len(enc)+len(kx.certBytes)+len(sig))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(enc)))
	binary.BigEndian.PutUint32(out[4:8], uint32(len(kx.certBytes)))
	copy(out[8:8+len(enc)], enc)
	copy(out[8+len(enc):8+len(enc)+len(kx.certBytes)], kx.certBytes)
	copy(out[len(out)-len(sig):], sig)
	// don't overwrite if already given key
	_, terr := kx.cli.Txn(kx.ctx).If(
		clientv3.Compare(clientv3.Version(string(kv.Key)), "=", 1),
	).Then(
		clientv3.OpPut(string(kv.Key), string(out), clientv3.WithIgnoreLease()),
	).Commit()
	return terr
}

// decryptSessionKey decrypts a response to own cert/ file and
// if the result is OK, uses it as the aes key.
func (kx *KeyExchange) decryptSessionKey(key []byte) error {
	if kx.sharedKey != nil {
		return nil
	}
	encsz := binary.BigEndian.Uint32(key[0:4])
	certsz := binary.BigEndian.Uint32(key[4:8])
	enc := key[8 : 8+encsz]
	certdat := key[8+encsz : 8+encsz+certsz]
	sig := key[8+encsz+certsz:]
	// get sender's cert
	block, _ := pem.Decode(certdat)
	cert, cerr := x509.ParseCertificate(block.Bytes)
	if cerr != nil {
		return cerr
	}
	// verify the sender's cert
	// TODO: revocation checks
	vopts := x509.VerifyOptions{Roots: kx.certpool}
	if _, verr := cert.Verify(vopts); verr != nil {
		return verr
	}
	// verify key's signature
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("bad public key type")
	}
	if verr := rsa.VerifyPSS(pub, crypto.SHA512_256, shaBytes(enc), sig, nil); verr != nil {
		return verr
	}
	// decrypt key
	sharedKey, serr := rsa.DecryptOAEP(sha512.New512_256(), rand.Reader, kx.privKey, enc, nil)
	if serr != nil {
		return serr
	}
	// pass back key
	kx.sharedKey = sharedKey
	kx.keyc <- kx.sharedKey
	return nil
}

func shaBytes(data []byte) []byte {
	shas := make([]byte, 32)
	for i, sha := 0, sha512.Sum512_256(data); i < len(sha); i++ {
		shas[i] = sha[i]
	}
	return shas
}
