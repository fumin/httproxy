package util

import (
	"crypto/rand"
	"math/big"

	"github.com/btcsuite/btcd/address/v2/base58"
)

func RandStr() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	a, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
	c := alphabet[a.Int64()]

	b := make([]byte, 64/8)
	rand.Read(b)
	s := base58.Encode(b)

	return string(c) + s
}
