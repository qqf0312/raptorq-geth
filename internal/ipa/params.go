package ipa

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// Params contains the public generators for this basic single inner product
// argument. The code uses additive notation: Com(b) is sum_i b_i * G_i.
type Params struct {
	G []bn254.G1Affine
	H []bn254.G1Affine
	U bn254.G1Affine
}

// NewTestParams deterministically derives test-only generators. It is suitable
// for unit tests and examples; production systems need a stricter generator
// derivation ceremony and domain separation policy.
func NewTestParams(n int) (*Params, error) {
	if err := validateVectorLen(n); err != nil {
		return nil, err
	}

	params := &Params{
		G: make([]bn254.G1Affine, n),
		H: make([]bn254.G1Affine, n),
	}
	for i := 0; i < n; i++ {
		g, err := testGenerator("ipa-test-G", i)
		if err != nil {
			return nil, err
		}
		h, err := testGenerator("ipa-test-H", i)
		if err != nil {
			return nil, err
		}
		params.G[i] = g
		params.H[i] = h
	}

	u, err := testGenerator("ipa-test-U", -1)
	if err != nil {
		return nil, err
	}
	params.U = u
	return params, nil
}

func testGenerator(domain string, idx int) (bn254.G1Affine, error) {
	label := []byte(domain)
	if idx >= 0 {
		label = append(label, '-')
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(idx))
		label = append(label, buf[:]...)
	}
	s, err := hashToScalar(label)
	if err != nil {
		return bn254.G1Affine{}, err
	}
	return pointScalarMul(baseG1(), s), nil
}

func baseG1() bn254.G1Affine {
	_, _, g1Aff, _ := bn254.Generators()
	return g1Aff
}

func hashToScalar(data []byte) (fr.Element, error) {
	for counter := uint32(0); ; counter++ {
		h := sha256.New()
		h.Write(data)
		var ctr [4]byte
		binary.BigEndian.PutUint32(ctr[:], counter)
		h.Write(ctr[:])

		var s fr.Element
		s.SetBytes(h.Sum(nil))
		if !s.IsZero() {
			return s, nil
		}
		if counter == ^uint32(0) {
			return fr.Element{}, fmt.Errorf("failed to derive non-zero scalar")
		}
	}
}
