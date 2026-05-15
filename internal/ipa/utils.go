package ipa

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func isPowerOfTwo(n int) bool {
	return n > 0 && n&(n-1) == 0
}

func log2(n int) int {
	l := 0
	for n > 1 {
		n >>= 1
		l++
	}
	return l
}

func validateVectorLen(n int) error {
	if n == 0 {
		return errors.New("empty vector")
	}
	if !isPowerOfTwo(n) {
		return fmt.Errorf("vector length %d is not a power of two", n)
	}
	return nil
}

func validateParams(params *Params, n int) error {
	if params == nil {
		return errors.New("nil params")
	}
	if len(params.G) < n {
		return fmt.Errorf("params G length %d is smaller than vector length %d", len(params.G), n)
	}
	if len(params.H) < n {
		return fmt.Errorf("params H length %d is smaller than vector length %d", len(params.H), n)
	}
	return nil
}

func scalarToBigInt(s fr.Element) *big.Int {
	var out big.Int
	s.BigInt(&out)
	return &out
}

func pointScalarMul(p bn254.G1Affine, s fr.Element) bn254.G1Affine {
	var out bn254.G1Affine
	out.ScalarMultiplication(&p, scalarToBigInt(s))
	return out
}

func pointAdd(points ...bn254.G1Affine) bn254.G1Affine {
	var acc bn254.G1Jac
	for i := range points {
		acc.AddMixed(&points[i])
	}
	var out bn254.G1Affine
	out.FromJacobian(&acc)
	return out
}

func pointEqual(a, b bn254.G1Affine) bool {
	return a.Equal(&b)
}

func multiScalarCommit(gens []bn254.G1Affine, scalars []fr.Element) (bn254.G1Affine, error) {
	if len(scalars) == 0 {
		return bn254.G1Affine{}, errors.New("empty vector")
	}
	if len(gens) < len(scalars) {
		return bn254.G1Affine{}, fmt.Errorf("generator length %d is smaller than scalar length %d", len(gens), len(scalars))
	}

	var acc bn254.G1Jac
	for i := range scalars {
		if scalars[i].IsZero() {
			continue
		}
		var term bn254.G1Jac
		term.ScalarMultiplicationAffine(&gens[i], scalarToBigInt(scalars[i]))
		acc.AddAssign(&term)
	}
	var out bn254.G1Affine
	out.FromJacobian(&acc)
	return out, nil
}

func foldScalars(left, right []fr.Element, leftWeight, rightWeight fr.Element) []fr.Element {
	out := make([]fr.Element, len(left))
	for i := range left {
		var l, r fr.Element
		l.Mul(&left[i], &leftWeight)
		r.Mul(&right[i], &rightWeight)
		out[i].Add(&l, &r)
	}
	return out
}

func foldGenerators(left, right []bn254.G1Affine, leftWeight, rightWeight fr.Element) []bn254.G1Affine {
	out := make([]bn254.G1Affine, len(left))
	for i := range left {
		l := pointScalarMul(left[i], leftWeight)
		r := pointScalarMul(right[i], rightWeight)
		out[i] = pointAdd(l, r)
	}
	return out
}

func splitScalars(v []fr.Element) ([]fr.Element, []fr.Element) {
	half := len(v) / 2
	return v[:half], v[half:]
}

func splitPoints(v []bn254.G1Affine) ([]bn254.G1Affine, []bn254.G1Affine) {
	half := len(v) / 2
	return v[:half], v[half:]
}
