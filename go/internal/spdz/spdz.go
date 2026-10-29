// Package spdz reimplements io.carbynestack:mp-spdz-integration (MpSpdzIntegrationUtils) with
// math/big: conversion between the 16 byte SPDZ gfp wire format (Montgomery form, limb swapped)
// and big integers, plus Java's UUID.nameUUIDFromBytes.
package spdz

import (
	"crypto/md5"
	"fmt"
	"math/big"
)

const (
	LimbWidth = 8
	WordWidth = 16
	// ShareWidth is value(16) || mac(16).
	ShareWidth = 32
)

// Utils holds the field parameters.
type Utils struct {
	Prime, R, RInv *big.Int
}

// New creates Utils for the given prime, Montgomery R and R^-1.
func New(prime, r, rInv *big.Int) *Utils { return &Utils{Prime: prime, R: r, RInv: rInv} }

func reverse(b []byte) []byte {
	o := make([]byte, len(b))
	for i := range b {
		o[len(b)-1-i] = b[i]
	}
	return o
}

func invertLimbEndianness(b []byte) []byte {
	o := make([]byte, 0, len(b))
	for i := 0; i < len(b); i += LimbWidth {
		o = append(o, reverse(b[i:i+LimbWidth])...)
	}
	return o
}

func swapLimbs(b []byte) []byte {
	o := make([]byte, 0, len(b))
	for i := len(b) - LimbWidth; i >= 0; i -= LimbWidth {
		o = append(o, b[i:i+LimbWidth]...)
	}
	return o
}

// FromGfp decodes a 16 byte gfp value. Errors carry the exact Java IllegalArgumentException text.
func (u *Utils) FromGfp(b []byte) (*big.Int, error) {
	if len(b) != WordWidth {
		return nil, fmt.Errorf("Gfp byte representation must have a length of %d. Actual: %d.", WordWidth, len(b))
	}
	x := new(big.Int).SetBytes(swapLimbs(invertLimbEndianness(b))) // unsigned big endian
	x.Mul(x, u.RInv)
	return x.Mod(x, u.Prime), nil
}

// ToGfp encodes v (0 <= v <= p) as a 16 byte gfp value.
func (u *Utils) ToGfp(v *big.Int) ([]byte, error) {
	if v.Cmp(u.Prime) > 0 {
		return nil, fmt.Errorf("Value must not be larger than %s. Actual: %s.", u.Prime, v)
	}
	if v.Sign() < 0 {
		return nil, fmt.Errorf("Value must not be negative. Actual: %s.", v)
	}
	m := new(big.Int).Mul(v, u.R)
	m.Mod(m, u.Prime)
	if m.BitLen() > WordWidth*8 {
		return nil, fmt.Errorf("value does not fit into %d bytes", WordWidth)
	}
	be := make([]byte, WordWidth)
	m.FillBytes(be)
	return swapLimbs(invertLimbEndianness(be)), nil
}

// NameUUIDFromBytes is java.util.UUID.nameUUIDFromBytes (version 3, MD5, no namespace).
func NameUUIDFromBytes(name []byte) string {
	d := md5.Sum(name)
	d[6] = d[6]&0x0f | 0x30
	d[8] = d[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", d[0:4], d[4:6], d[6:8], d[8:10], d[10:16])
}
