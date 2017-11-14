package etcdircd

import (
	"bytes"
)

var userModes = map[byte]struct{}{
	// Away
	'a': {},
	// Invisible
	'i': {},
	// Local operator
	'o': {},
	// OTR encrypted
	'E': {},
	// Global operator
	'O': {},
}

var userAddModes = map[byte]struct{}{
	'i': {},
	'E': {},
}

var userDelModes = map[byte]struct{}{
	'a': {},
	'i': {},
	'o': {},
	'E': {},
	'O': {},
}

type ModeValue []byte

func newModeValue(modes string) ModeValue { return ModeValue(modes) }

func (mv ModeValue) has(m byte) bool { return bytes.IndexByte(mv, m) >= 0 }

func (mv ModeValue) add(m byte) ModeValue {
	if mv.has(m) {
		return mv
	}
	return append(mv, m)
}

func (mv ModeValue) del(m byte) ModeValue {
	idx := bytes.IndexByte(mv, m)
	if idx < 0 {
		return mv
	}
	return append(mv[:idx], mv[idx+1:]...)
}

// update interprets mode updates of form '+whatever' and '-whatever'.
// Returns a list of uninterpreted modes.
func (mv ModeValue) update(modeStr string) (_ ModeValue, bad []byte) {
	ms := []byte(modeStr)
	switch ms[0] {
	case '+':
		for _, m := range ms[1:] {
			if _, ok := userAddModes[m]; ok {
				mv = mv.add(m)
			} else {
				bad = append(bad, m)
			}
		}
	case '-':
		for _, m := range ms[1:] {
			if _, ok := userDelModes[m]; ok {
				mv = mv.del(m)
			} else {
				bad = append(bad, m)
			}
		}
	default:
		for _, m := range ms {
			if _, ok := userModes[m]; !ok {
				bad = append(bad, m)
			}
		}
	}
	return mv, bad
}

func (mv ModeValue) Equal(mv2 ModeValue) bool { return bytes.Equal(mv, mv2) }
