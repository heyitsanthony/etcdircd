package main

import (
	"fmt"
	"os"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/pkg/transport"
	"github.com/spf13/cobra"

	"github.com/heyitsanthony/etcdircd/internal/util"
)

var (
	rootCmd = &cobra.Command{
		Use:   "ircdctl",
		Short: "A simple command line client for etcdircd.",
	}
	flagConfigPath        string
	flagEtcdConfigPath    string
	flagDisableEtcdCrypto bool

	flagTarget     string
	flagOperGlobal bool
)

func init() {
	cobra.EnablePrefixMatching = true

	rootCmd.PersistentFlags().StringVar(
		&flagConfigPath,
		"etcdircd-yaml",
		"",
		"etcdircd configuration file",
	)
	rootCmd.PersistentFlags().StringVar(
		&flagConfigPath,
		"etcdclient-yaml",
		"",
		"etcd client configuration file",
	)
	rootCmd.PersistentFlags().BoolVar(
		&flagDisableEtcdCrypto,
		"disable-etcdcrypto",
		false,
		"store data in etcd plaintext")
	rootCmd.AddCommand(newNoticeCommand())
	rootCmd.AddCommand(newOperCommand())
}

func mustClient(cmd *cobra.Command) *etcd.Client {
	ircCfg, err := util.NewIrcCfgFromFile(flagConfigPath)
	exitOnErr(err)

	cli := util.MustEtcdClient(flagEtcdConfigPath)

	var tlsinfo *transport.TLSInfo
	if !flagDisableEtcdCrypto {
		tlsinfo = &transport.TLSInfo{
			KeyFile:  ircCfg.EtcdCryptoKeyFile,
			CertFile: ircCfg.EtcdCryptoCertFile,
			CAFile:   ircCfg.EtcdCryptoCAFile,
		}
	}
	return util.MustIrcdEtcdClient(cli, ircCfg.EtcdPrefix, tlsinfo)
}

func exitOnErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%+v\n", err)
		os.Exit(1)
	}
}

func main() {
	exitOnErr(rootCmd.Execute())
}
