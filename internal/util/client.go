package util

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"hash"
	"time"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/clientv3/namespace"
	etcdyaml "github.com/coreos/etcd/clientv3/yaml"
	"github.com/coreos/etcd/pkg/transport"
	"github.com/heyitsanthony/etcdcrypto"

	"github.com/heyitsanthony/etcdircd"
	"github.com/heyitsanthony/etcdircd/keyencode"
)

func MustIrcdEtcdClient(cli *etcd.Client, pfx string, tlsinfo *transport.TLSInfo) *etcd.Client {
	// Add namespaces and encryption to the client before passing to etcdircd.
	xcli := etcd.NewCtxClient(cli.Ctx())
	xcli.KV = cli.KV
	xcli.Watcher = cli.Watcher
	xcli.Lease = cli.Lease
	if pfx != "" {
		xcli.KV = namespace.NewKV(xcli.KV, pfx)
		xcli.Watcher = namespace.NewWatcher(xcli.Watcher, pfx)
	}
	// Update client with secure kv, if requested.
	if tlsinfo != nil {
		MustCrypto(xcli, *tlsinfo)
	}
	return xcli
}

func MustEtcdClient(etcdYamlPath string) *etcd.Client {
	etcdCfg := etcd.Config{Endpoints: []string{"http://localhost:2379"}}
	if etcdYamlPath != "" {
		fcfg, err := etcdyaml.NewConfig(etcdYamlPath)
		if err != nil {
			panic(err)
		}
		etcdCfg = *fcfg
	}
	cli, err := etcd.New(etcdCfg)
	if err != nil {
		panic(err)
	}
	return cli
}

func MustCrypto(cli *etcd.Client, tlsinfo transport.TLSInfo) {
	xcli := etcd.NewCtxClient(cli.Ctx())
	xcli.KV = cli.KV
	xcli.Watcher = cli.Watcher
	xcli.Lease = cli.Lease

	// Negotiate AES key.
	kx, kxerr := etcdcrypto.NewKeyExchange(xcli, "", tlsinfo)
	if kxerr != nil {
		panic(kxerr)
	}
	var aeskey []byte
	select {
	case aeskey = <-kx.SymmetricKey():
	case <-time.After(5 * time.Second):
		panic("could not establish session key")
	}
	cipher, cerr := etcdcrypto.NewAESCipher(aeskey)
	if cerr != nil {
		panic(cerr)
	}

	// Set up key encryption.
	h := func() hash.Hash { return hmac.New(sha256.New, aeskey) }
	b, cerr := aes.NewCipher(aeskey)
	if cerr != nil {
		panic(cerr)
	}
	encoder := etcdircd.NewKeyEncoder(keyencode.NewKeyEncoderHMAC(h, b))

	// Encrypt keys and values.
	encKv := etcdcrypto.NewKV(cli.KV, cipher)
	encWatcher := etcdcrypto.NewWatcher(cli.Watcher, cipher)
	cli.KV = keyencode.NewKV(encKv, encoder)
	cli.Watcher = keyencode.NewWatcher(encWatcher, encoder)
}
