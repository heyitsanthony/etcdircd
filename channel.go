package etcdircd

import (
	"bytes"
	"encoding/gob"
	"strings"
	"time"
)

// ChannelCtl represents a channel in etcd.
type ChannelCtl struct {
	Name    string
	Topic   string
	Mode    string
	Created time.Time
}

type channelNick struct {
	Nick       string
	User       string
	Host       string
	ServerName string
	Realname   string
}

// ChannelNicks is one per server session; holds all nick data.
type ChannelNicks struct {
	Nicks []channelNick
}

func encodeChannelCtl(cc ChannelCtl) string {
	buf := bytes.NewBuffer(make([]byte, 0, 256))
	enc := gob.NewEncoder(buf)
	if err := enc.Encode(&cc); err != nil {
		panic(err)
	}
	return buf.String()
}

func decodeChannelCtl(cc string) (*ChannelCtl, error) {
	dec := gob.NewDecoder(strings.NewReader(cc))
	v := &ChannelCtl{}
	if err := dec.Decode(v); err != nil {
		return nil, err
	}
	return v, nil
}

func encodeChannelNicks(cn ChannelNicks) string {
	buf := bytes.NewBuffer(make([]byte, 0, 256))
	enc := gob.NewEncoder(buf)
	if err := enc.Encode(&cn); err != nil {
		panic(err)
	}
	return buf.String()
}

func decodeChannelNicks(cn string) (*ChannelNicks, error) {
	dec := gob.NewDecoder(strings.NewReader(cn))
	v := &ChannelNicks{}
	if err := dec.Decode(v); err != nil {
		return nil, err
	}
	return v, nil
}
