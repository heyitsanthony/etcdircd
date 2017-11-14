package main

import (
	"fmt"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/spf13/cobra"

	"github.com/heyitsanthony/etcdircd"
)

func newOperCommand() *cobra.Command {
	operCmd := &cobra.Command{
		Use:   "oper <subcommand>",
		Short: "Operator related commands",
	}
	operMkCmd := &cobra.Command{
		Use:   "mk <login> <pass>",
		Short: "make an operator",
		Run:   runOperMk,
	}
	operMkCmd.Flags().BoolVar(&flagOperGlobal, "global", false, "set global privileges")
	operRmCmd := &cobra.Command{
		Use:   "rm <login>",
		Short: "remove an operator",
		Run:   runOperRm,
	}
	operLsCmd := &cobra.Command{
		Use:   "ls",
		Short: "list operators",
		Run:   runOperLs,
	}
	operCmd.AddCommand(operMkCmd)
	operCmd.AddCommand(operRmCmd)
	operCmd.AddCommand(operLsCmd)
	return operCmd
}

func runOperMk(cmd *cobra.Command, args []string) {
	if len(args) != 2 {
		exitOnErr(fmt.Errorf("oper mk expected two arguments, got %q", args))
	}
	cli := mustClient(cmd)
	defer cli.Close()

	login := args[0]
	ov := etcdircd.OperValue{
		Pass:   args[1],
		Global: flagOperGlobal,
	}

	_, err := cli.Put(cli.Ctx(), etcdircd.KeyOperCtl(login), ov.Encode())
	exitOnErr(err)
}

func runOperRm(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		exitOnErr(fmt.Errorf("oper rm expected one argument, got %q", args))
	}
	cli := mustClient(cmd)
	defer cli.Close()

	_, err := cli.Delete(cli.Ctx(), etcdircd.KeyOperCtl(args[0]))
	exitOnErr(err)
}

func runOperLs(cmd *cobra.Command, args []string) {
	cli := mustClient(cmd)
	defer cli.Close()

	resp, err := cli.Get(cli.Ctx(), etcdircd.KeyOperCtl(""), etcd.WithPrefix())
	exitOnErr(err)

	for _, kv := range resp.Kvs {
		ov, err := etcdircd.NewOperValue(string(kv.Value))
		exitOnErr(err)
		fmt.Printf("%v: %+v\n", string(kv.Key), ov)
	}
}
