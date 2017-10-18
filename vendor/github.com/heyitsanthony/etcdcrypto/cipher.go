package etcdcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
)

type Cipher interface {
	Encrypt([]byte) []byte
	Decrypt([]byte) ([]byte, error)
}

func NewAESCipher(aesKey []byte) (Cipher, error) {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	aead, aerr := cipher.NewGCM(block)
	if aerr != nil {
		return nil, aerr
	}
	return NewAEADCipher(aead), nil
}

type aeadCipher struct {
	aead cipher.AEAD
}

func NewAEADCipher(aead cipher.AEAD) Cipher { return &aeadCipher{aead} }

func (c *aeadCipher) Encrypt(v []byte) []byte {
	ns := c.aead.NonceSize()
	nonce := make([]byte, ns, ns+len(v)+c.aead.Overhead())
	if _, err := rand.Read(nonce[:ns]); err != nil {
		panic(err)
	}
	return append(nonce, c.aead.Seal(nil, nonce, v, nil)...)
}

func (c *aeadCipher) Decrypt(v []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	return c.aead.Open(nil, v[:ns], v[ns:], nil)
}
