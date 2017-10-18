package main

import (
	"crypto/tls"
	"flag"
	"net"
	"os"

	"github.com/coreos/etcd/pkg/transport"
	"github.com/golang/glog"

	"github.com/heyitsanthony/etcdircd"
	"github.com/heyitsanthony/etcdircd/internal/util"
)

func main() {
	etcdYaml := flag.String("etcdclient-yaml", "", "etcd v3 client yaml config")
	ircYaml := flag.String("etcdircd-yaml", "", "irc yaml config")
	listen := flag.String("listen", "127.0.0.1:6667", "address for accepting IRC connections")

	// These should really never be enabled outside of testing.
	noEtcdCrypto := flag.Bool("disable-etcdcrypto", false, "store data in etcd plaintext")
	noIRCTLS := flag.Bool("disable-irc-tls", false, "allow plaintext tls connections")

	// TODO: enforce OTR for privmsg

	flag.Parse()

	if len(os.Args) == 1 {
		glog.Warning("unsafe demo mode! please generate certs")
		*noEtcdCrypto = true
		*noIRCTLS = true
	}

	ircCfg, err := util.NewIrcCfgFromFile(*ircYaml)
	if err != nil {
		panic(err)
	}
	glog.Infof("host: %s", ircCfg.HostName)
	glog.Infof("network: %s", ircCfg.NetworkName)

	if ircCfg.IrcdClientCerts {
		panic("no support for ircd client certs")
	}

	ln, lerr := net.Listen("tcp", *listen)
	if lerr != nil {
		panic(lerr)
	}
	if !*noIRCTLS {
		if ircCfg.IrcdKeyFile == "" {
			panic("no IRC TLS defined")
		}
		tlsinfo := &transport.TLSInfo{
			KeyFile:  ircCfg.IrcdKeyFile,
			CertFile: ircCfg.IrcdCertFile,
			CAFile:   ircCfg.IrcdCAFile,
		}
		tlscfg, err := tlsinfo.ServerConfig()
		if err != nil {
			panic(err)
		}
		if ircCfg.IrcdClientCerts {
			tlscfg.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			tlscfg.ClientAuth = tls.NoClientCert
		}
		ln = tls.NewListener(ln, tlscfg)
	}

	cli := util.MustEtcdClient(*etcdYaml)
	defer cli.Close()

	var tlsinfo *transport.TLSInfo
	if !*noEtcdCrypto {
		tlsinfo = &transport.TLSInfo{
			KeyFile:  ircCfg.EtcdCryptoKeyFile,
			CertFile: ircCfg.EtcdCryptoCertFile,
			CAFile:   ircCfg.EtcdCryptoCAFile,
		}
	}
	scli := util.MustIrcdEtcdClient(cli, ircCfg.EtcdPrefix, tlsinfo)
	defer scli.Close()

	// Run the server.
	glog.Infof("etcdircd listening on %v; backed by %v\n", *listen, cli.Endpoints())
	panic(etcdircd.NewServer(ircCfg.Config, ln, scli).Serve())
}
