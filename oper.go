package etcdircd

import (
	"bytes"
	"encoding/gob"
	"strings"
)

type OperValue struct {
	Pass   string
	Global bool
}

func encodeOperValue(ov OperValue) string {
	buf := bytes.NewBuffer(make([]byte, 0, 256))
	enc := gob.NewEncoder(buf)
	if err := enc.Encode(&ov); err != nil {
		panic(err)
	}
	return buf.String()
}

func decodeOperValue(ov string) (*OperValue, error) {
	dec := gob.NewDecoder(strings.NewReader(ov))
	v := &OperValue{}
	if err := dec.Decode(v); err != nil {
		return nil, err
	}
	return v, nil
}
