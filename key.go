package etcdircd

import (
	"fmt"
	"strings"

	"github.com/heyitsanthony/etcdircd/keyencode"
)

const (
	keyIdxType int = iota + 1
	keyIdxSubtype
	keyIdxData

//	keyIdxSessions
)

func KeyUserMsg(n string) string { return keyUserMsg(n) }

func keyUserCtl(n string) string { return "/user/ctl/" + n }
func keyUserMsg(n string) string { return "/user/msg/" + n }

func KeyChanMsg(ch string) string { return keyChanMsg(ch) }

func keyChanCtl(ch string) string             { return "/chan/ctl/" + ch }
func keyChanMsg(ch string) string             { return "/chan/msg/" + ch }
func keyChanNicksPfx(ch string) string        { return "/chan/nicks/" + ch + "/" }
func keyChanNicks(ch string, id int64) string { return fmt.Sprintf("/chan/nicks/%s/%x", ch, id) }

func IsChan(ch string) bool { return isChan(ch) }

func isChan(ch string) bool {
	// TODO: filter more characers
	if len(ch) == 0 {
		return false
	}
	return ch[0] == '#'
}

type keyEncoder struct{ ke keyencode.KeyEncoder }

// NewKeyEncoder creates a key encoder that will encode sensitive parts
// of a given key.
func NewKeyEncoder(ke keyencode.KeyEncoder) keyencode.KeyEncoder {
	return &keyEncoder{ke}
}

func splitKey(s string) []string {
	ss := strings.Split(s, "/")
	if len(ss) <= keyIdxData {
		panic(s)
	}
	// /{chan,user}/{msg,ctl,nicks}/<encrypted>/whatever
	switch ss[keyIdxType] {
	case "user":
		switch ss[keyIdxSubtype] {
		case "msg":
		case "ctl":
		default:
			panic(s)
		}
	case "chan":
		switch ss[keyIdxSubtype] {
		case "msg":
		case "ctl":
		case "nicks":
		default:
			panic(s)
		}
	default:
		panic(s)
	}
	return ss
}

func (ke *keyEncoder) Encode(s string) string {
	ss := splitKey(s)
	if len(ss[keyIdxData]) == 0 {
		return s
	}
	// Encode the entire prefix so /a/123 and /b/123 will gives /a/X and /b/Y
	// when encrypted.
	sse := ke.ke.Encode(strings.Join(ss[:keyIdxData+1], "/"))
	// Track length of encoded segment for decode later.
	ss[3] = fmt.Sprintf("%d/%s", len(sse), sse)
	v := strings.Join(ss, "/")
	return v
}

func (ke *keyEncoder) Decode(s string) (string, error) {
	ss := splitKey(s)
	// Encoded segment's length is in /a/b/<len>
	enclen := 0
	if n, err := fmt.Sscanf(ss[keyIdxData], "%d", &enclen); n != 1 || err != nil {
		panic(err)
	}
	// Compute the length for prefix /a/b/<len>
	pfxlen := len(strings.Join(ss[:keyIdxData+1], "/"))
	// Decode encoded segment following the prefix.
	dec, err := ke.ke.Decode(s[pfxlen+1 : pfxlen+enclen+1])
	if err != nil {
		return "", err
	}
	dec = strings.Split(dec, "/")[keyIdxData]
	// Stitch it back together.
	v := "/" + ss[keyIdxType] + "/" + ss[keyIdxSubtype] + "/" + dec + s[pfxlen+enclen+1:]
	return v, nil
}
