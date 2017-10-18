package util

import (
	"io/ioutil"

	"github.com/ghodss/yaml"

	"github.com/heyitsanthony/etcdircd"
)

type IrcCfg struct {
	etcdircd.Config

	EtcdPrefix string

	EtcdCryptoKeyFile  string
	EtcdCryptoCertFile string
	EtcdCryptoCAFile   string

	IrcdKeyFile  string
	IrcdCertFile string
	IrcdCAFile   string

	IrcdClientCerts bool
}

func NewIrcCfgFromFile(f string) (*IrcCfg, error) {
	cfg := &IrcCfg{Config: etcdircd.NewConfig(), EtcdPrefix: "/irc/"}
	if f == "" {
		return cfg, nil
	}
	b, err := ioutil.ReadFile(f)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
