package main

import (
	"fmt"
	"os"
	"sync"
	"strings"

	"github.com/spf13/cobra"
	etcd "github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/pkg/transport"
	"gopkg.in/sorcix/irc.v2"

	"github.com/heyitsanthony/etcdircd/internal/util"
	"github.com/heyitsanthony/etcdircd"
)

var (
	rootCmd = &cobra.Command{
		Use:   "ircdctl",
		Short: "A simple command line client for etcdircd.",
	}
	flagTarget string
	flagConfigPath string
	flagEtcdConfigPath string
	flagDisableEtcdCrypto bool
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
	noticeCmd := &cobra.Command{
		Use:   "notice <message>",
		Short: "notices the network with message",
		Run:   runNotice,
	}
	noticeCmd.Flags().StringVar(&flagTarget, "target", "", "direct notice to particular user or channel (default global)")
	rootCmd.AddCommand(
		noticeCmd,
	)
}

func mustClient(cmd *cobra.Command) *etcd.Client {
	ircCfg, err := util.NewIrcCfgFromFile(flagConfigPath)
	exitOnErr(err)

	cli := util.MustEtcdClient(flagEtcdConfigPath)

	var tlsinfo *transport.TLSInfo
	if !flagDisableEtcdCrypto {
		tlsinfo = &transport.TLSInfo{
			KeyFile: ircCfg.EtcdCryptoKeyFile,
			CertFile: ircCfg.EtcdCryptoCertFile,
			CAFile: ircCfg.EtcdCryptoCAFile,
		}
	}
	return util.MustIrcdEtcdClient(cli, ircCfg.EtcdPrefix, tlsinfo)
}

func runNotice(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		exitOnErr(fmt.Errorf("notice expected one message argument, got more %q", args))
	}
	cli := mustClient(cmd)
	defer cli.Close()

	msgf := func(k string) {
		ks := strings.Split(k, "/")
		nick := ks[len(ks)-1]
		msg := irc.Message{
			Prefix: &irc.Prefix{Name: "ircdctl"},
			Command: irc.NOTICE,
			Params: []string{nick, args[0]},
		}
		resp, err := cli.Txn(cli.Ctx()).If(
			etcd.Compare(etcd.Version(k), ">", 0),
		).Then(
			etcd.OpPut(k, etcdircd.EncodeMessage(msg), etcd.WithIgnoreLease()),
		).Commit()
		exitOnErr(err)
		if !resp.Succeeded {
			fmt.Println(k, "does not exist")
		}
	}

	if flagTarget != "" {
		if !etcdircd.IsChan(flagTarget) {
			msgf(etcdircd.KeyUserMsg(flagTarget))
		} else {
			msgf(etcdircd.KeyChanMsg(flagTarget))
		}
		return
	}

	// Fetch all user names, then write out.
	resp, err := cli.Get(cli.Ctx(), etcdircd.KeyUserMsg(""), etcd.WithPrefix())
	exitOnErr(err)
	var wg sync.WaitGroup
	wg.Add(len(resp.Kvs))
	for i := range resp.Kvs {
		kv := resp.Kvs[i]
		go func() {
			defer wg.Done()
			msgf(string(kv.Key))
		}()
	}
	wg.Wait()
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
