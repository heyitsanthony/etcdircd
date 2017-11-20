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
	Mode    ModeValue
	Created time.Time
}

// ChannelNick contains per-channel information for a nick.
type ChannelNick struct {
	Nick string
	Mode ModeValue
}

// ChannelNicks is one per server session; holds all nick data for a channel.
type ChannelNicks struct {
	Nicks []ChannelNick
}

func (cns *ChannelNicks) nicks() []string {
	ret := make([]string, len(cns.Nicks))
	for i, cn := range cns.Nicks {
		ret[i] = cn.Nick
	}
	return ret
}

func (cns *ChannelNicks) find(nick string) *ChannelNick {
	for i, cn := range cns.Nicks {
		if cn.Nick == nick {
			return &cns.Nicks[i]
		}
	}
	return nil
}

func (cu *ChannelNicks) del(nick string) bool {
	for i, u := range cu.Nicks {
		if u.Nick == nick {
			cu.Nicks = append(cu.Nicks[:i], cu.Nicks[i+1:]...)
			return true
		}
	}
	return false
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
