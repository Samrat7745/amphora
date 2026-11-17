// Package spdz wraps the Go port of io.carbynestack:mp-spdz-integration (MpSpdzIntegrationUtils):
// conversion between the 16 byte SPDZ gfp wire format (Montgomery form, limb swapped)
// and big integers, plus Java's UUID.nameUUIDFromBytes.
package spdz

import (
	"crypto/md5"
	"fmt"
	"math/big"
	"sync"

	mpspdz "github.com/Samrat7745/mp-spdz-integration"
)

const (
	LimbWidth = mpspdz.LimbWidth
	WordWidth = mpspdz.WordWidth
	// ShareWidth is value(16) || mac(16).
	ShareWidth = mpspdz.ShareWidth
)

// Utils holds the field parameters. The conversions are done by the mp-spdz-integration library;
// this type keeps the exported fields and the Java exception texts the service relies on.
type Utils struct {
	Prime, R, RInv *big.Int

	once sync.Once
	impl *mpspdz.IntegrationUtils
	err  error
}

// New creates Utils for the given prime, Montgomery R and R^-1.
func New(prime, r, rInv *big.Int) *Utils { return &Utils{Prime: prime, R: r, RInv: rInv} }

// lib returns the mp-spdz-integration converter for the field parameters (built on first use).
func (u *Utils) lib() (*mpspdz.IntegrationUtils, error) {
	u.once.Do(func() { u.impl, u.err = mpspdz.New(u.Prime, u.R, u.RInv) })
	return u.impl, u.err
}

// FromGfp decodes a 16 byte gfp value. Errors carry the exact Java IllegalArgumentException text.
func (u *Utils) FromGfp(b []byte) (*big.Int, error) {
	if len(b) != WordWidth {
		return nil, fmt.Errorf("Gfp byte representation must have a length of %d. Actual: %d.", WordWidth, len(b))
	}
	lib, err := u.lib()
	if err != nil {
		return nil, err
	}
	return lib.FromGfp(b)
}

// ToGfp encodes v (0 <= v <= p) as a 16 byte gfp value.
func (u *Utils) ToGfp(v *big.Int) ([]byte, error) {
	if v.Cmp(u.Prime) > 0 {
		return nil, fmt.Errorf("Value must not be larger than %s. Actual: %s.", u.Prime, v)
	}
	if v.Sign() < 0 {
		return nil, fmt.Errorf("Value must not be negative. Actual: %s.", v)
	}
	// The library truncates Montgomery values wider than a word; report them instead.
	if u.Prime.BitLen() > WordWidth*8 {
		m := new(big.Int).Mul(v, u.R)
		if m.Mod(m, u.Prime).BitLen() > WordWidth*8 {
			return nil, fmt.Errorf("value does not fit into %d bytes", WordWidth)
		}
	}
	lib, err := u.lib()
	if err != nil {
		return nil, err
	}
	return lib.ToGfp(v)
}

// NameUUIDFromBytes is java.util.UUID.nameUUIDFromBytes (version 3, MD5, no namespace).
func NameUUIDFromBytes(name []byte) string {
	d := md5.Sum(name)
	d[6] = d[6]&0x0f | 0x30
	d[8] = d[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", d[0:4], d[4:6], d[6:8], d[8:10], d[10:16])
}
