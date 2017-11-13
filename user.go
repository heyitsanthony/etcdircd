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
	AwayMsg  string
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

func stringSliceToMap(ss []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}

func pruneInvisible(users []UserValue, me UserValue) []UserValue {
	if me.Mode.has('o') {
		return users
	}
	ret := []UserValue{}
	uchan := stringSliceToMap(me.Channels)
	for _, user := range users {
		if !user.Mode.has('i') {
			ret = append(ret, user)
			continue
		}
		sharedChannels := []string{}
		for _, ch := range user.Channels {
			if _, ok := uchan[ch]; ok {
				sharedChannels = append(sharedChannels, ch)
			}
		}
		if len(sharedChannels) > 0 {
			user.Channels = sharedChannels
			ret = append(ret, user)
		}
	}
	return ret
}
