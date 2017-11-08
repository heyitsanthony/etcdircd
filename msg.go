package etcdircd

import (
	"bytes"
	"encoding/gob"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func EncodeMessage(m irc.Message) string { return encodeMessage(m) }

func encodeMessage(m irc.Message) string {
	buf := bytes.NewBuffer(make([]byte, 0, 256))
	enc := gob.NewEncoder(buf)
	if err := enc.Encode(&m); err != nil {
		panic(err)
	}
	return buf.String()
}

func decodeMessage(m string) (*irc.Message, error) {
	dec := gob.NewDecoder(strings.NewReader(m))
	v := &irc.Message{}
	if err := dec.Decode(v); err != nil {
		return nil, err
	}
	return v, nil
}
