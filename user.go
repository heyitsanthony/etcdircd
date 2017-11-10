package etcdircd

import (
	"bytes"
	"encoding/gob"
	"strings"
	"time"
)

type UserValue struct {
	Nick       string
	User       []string
	ServerName string
	Mode       ModeValue
	Host       string
	RealName   string

	Created  time.Time
	Channels []string
}

func encodeUserValue(uv UserValue) string {
	buf := bytes.NewBuffer(make([]byte, 0, 256))
	enc := gob.NewEncoder(buf)
	if err := enc.Encode(&uv); err != nil {
		panic(err)
	}
	return buf.String()
}

func decodeUserValue(uv string) (*UserValue, error) {
	dec := gob.NewDecoder(strings.NewReader(uv))
	v := &UserValue{}
	if err := dec.Decode(v); err != nil {
		return nil, err
	}
	return v, nil
}
