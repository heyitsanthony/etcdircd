package etcdircd

import (
	"bytes"
)

type modeSet map[byte]struct{}

type modePerms struct {
	all modeSet
	add modeSet
	del modeSet
}

var userModePerms = modePerms{
	all: modeSet{
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
	},
	add: modeSet{
		'i': {},
		'E': {},
	},
	del: modeSet{
		'a': {},
		'i': {},
		'o': {},
		'E': {},
		'O': {},
	},
}

var chanModes = modeSet{
	// need op or voice to send message on channel
	'm': {},
	// need op or voice to set topic
	't': {},
	// do not display in LIST output
	's': {},
}

var chanModePerms = modePerms{
	all: chanModes,
	add: chanModes,
	del: chanModes,
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
func (mv ModeValue) update(modeStr string, mp modePerms) (_ ModeValue, bad []byte) {
	ms := []byte(modeStr)
	if len(ms) == 0 {
		return mv, nil
	}
	switch ms[0] {
	case '+':
		for _, m := range ms[1:] {
			if _, ok := mp.add[m]; ok {
				mv = mv.add(m)
			} else {
				bad = append(bad, m)
			}
		}
	case '-':
		for _, m := range ms[1:] {
			if _, ok := mp.del[m]; ok {
				mv = mv.del(m)
			} else {
				bad = append(bad, m)
			}
		}
	default:
		for _, m := range ms {
			if _, ok := mp.all[m]; !ok {
				bad = append(bad, m)
			}
		}
	}
	return mv, bad
}

func (mv ModeValue) updateSlice(modes []string, mp modePerms) (ModeValue, []byte, bool) {
	bad := []byte{}
	oldMode, ret := mv, mv
	for _, m := range modes {
		newMode, newBad := ret.update(m, mp)
		ret = newMode
		bad = append(bad, newBad...)
	}
	return ret, bad, !oldMode.Equal(ret)
}

func (mv ModeValue) Equal(mv2 ModeValue) bool { return bytes.Equal(mv, mv2) }
