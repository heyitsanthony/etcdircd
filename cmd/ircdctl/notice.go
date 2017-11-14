package main

import (
	"fmt"
	"strings"
	"sync"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/spf13/cobra"
	"gopkg.in/sorcix/irc.v2"

	"github.com/heyitsanthony/etcdircd"
)

func newNoticeCommand() *cobra.Command {
	noticeCmd := &cobra.Command{
		Use:   "notice <message>",
		Short: "notices the network with message",
		Run:   runNotice,
	}
	noticeCmd.Flags().StringVar(&flagTarget, "target", "", "direct notice to particular user or channel (default global)")
	return noticeCmd
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
			Prefix:  &irc.Prefix{Name: "ircdctl"},
			Command: irc.NOTICE,
			Params:  []string{nick, args[0]},
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
