package keyencode

import (
	"crypto/cipher"
	"crypto/hmac"
	"fmt"
	"hash"
)

// KeyEncoder transforms a string 1:1 between encoded and decoded representations.
type KeyEncoder interface {
	Encode(s string) string
	Decode(s string) (string, error)
}

type nopKeyEncoder struct{}

func (n *nopKeyEncoder) Encode(s string) string          { return s }
func (n *nopKeyEncoder) Decode(s string) (string, error) { return s, nil }

func NewKeyEncoderNop() KeyEncoder {
	return &nopKeyEncoder{}
}

type hmacKeyEncoder struct {
	h func() hash.Hash
	b cipher.Block
}

// NewKeyEncoderHMAC use an hmac hash and a block cipher to
// encode keys.
func NewKeyEncoderHMAC(h func() hash.Hash, b cipher.Block) KeyEncoder {
	return &hmacKeyEncoder{h, b}
}

func (hke *hmacKeyEncoder) Encode(s string) string {
	// Compute IV from plaintext without padding
	hf := hke.h()
	ptxt := []byte(s)
	if _, err := hf.Write(ptxt); err != nil {
		panic(err)
	}
	ptxtHash := hf.Sum(nil)

	// pkcs7 encode string into cipher block size.
	bs := hke.b.BlockSize()
	padding := make([]byte, bs-(len(s)%bs))
	for i := range padding {
		padding[i] = byte(len(padding))
	}
	ptxt = append(ptxt, padding...)

	// Encrypt with cipher
	iv := ptxtHash[len(ptxtHash)-bs:]
	ctr := cipher.NewCTR(hke.b, iv)
	ctxt := make([]byte, len(ptxtHash)+len(ptxt))
	copy(ctxt[:len(ptxtHash)], ptxtHash)
	ctr.XORKeyStream(ctxt[len(ptxtHash):], ptxt)

	return string(ctxt)
}

func (hke *hmacKeyEncoder) Decode(s string) (string, error) {
	// Extract hmac and ctxt
	hf := hke.h()
	ptxtHash := []byte(s[:hf.Size()])
	ctxt := []byte(s[hf.Size():])

	// Decrypt with cipher
	bs := hke.b.BlockSize()
	iv := ptxtHash[len(ptxtHash)-bs:]
	ctr := cipher.NewCTR(hke.b, iv)
	ptxt := make([]byte, len(ctxt))
	ctr.XORKeyStream(ptxt, ctxt)

	// pkcs7 decode to string length.
	padlen := int(ptxt[len(ptxt)-1])
	if padlen > bs {
		return "", fmt.Errorf("bad pkcs7 %d", padlen)
	}
	ptxt = ptxt[:len(ptxt)-padlen]

	// Compute hmac from decrypted plaintext.
	hf.Write(ptxt)
	h := hf.Sum(nil)

	// Check given hmac matches computed hmac.
	if !hmac.Equal(h, ptxtHash) {
		return "", fmt.Errorf("bad hmac")
	}

	return string(ptxt), nil
}
