package ipa

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// InnerProduct computes <a,b> over bn254/fr.
func InnerProduct(a, b []fr.Element) (fr.Element, error) {
	if len(a) == 0 || len(b) == 0 {
		return fr.Element{}, fmt.Errorf("empty vector")
	}
	if len(a) != len(b) {
		return fr.Element{}, fmt.Errorf("vector length mismatch: %d != %d", len(a), len(b))
	}
	if err := validateVectorLen(len(a)); err != nil {
		return fr.Element{}, err
	}

	var out fr.Element
	for i := range a {
		var term fr.Element
		term.Mul(&a[i], &b[i])
		out.Add(&out, &term)
	}
	return out, nil
}

// CommitB binds the secret vector b as Q = sum_i b_i * G_i.
func CommitB(params *Params, b []fr.Element) (bn254.G1Affine, error) {
	if err := validateVectorLen(len(b)); err != nil {
		return bn254.G1Affine{}, err
	}
	if err := validateParams(params, len(b)); err != nil {
		return bn254.G1Affine{}, err
	}
	return multiScalarCommit(params.G, b)
}

// CommitA computes the public contribution sum_i a_i * H_i.
func CommitA(params *Params, a []fr.Element) (bn254.G1Affine, error) {
	if err := validateVectorLen(len(a)); err != nil {
		return bn254.G1Affine{}, err
	}
	if err := validateParams(params, len(a)); err != nil {
		return bn254.G1Affine{}, err
	}
	return multiScalarCommit(params.H, a)
}

// ComputeP reconstructs P from public data: P = CommitA(a) + Q + c * U.
func ComputeP(params *Params, a []fr.Element, Q bn254.G1Affine, c fr.Element) (bn254.G1Affine, error) {
	if err := validateVectorLen(len(a)); err != nil {
		return bn254.G1Affine{}, err
	}
	if err := validateParams(params, len(a)); err != nil {
		return bn254.G1Affine{}, err
	}
	aCommit, err := CommitA(params, a)
	if err != nil {
		return bn254.G1Affine{}, err
	}
	cU := pointScalarMul(params.U, c)
	return pointAdd(aCommit, Q, cU), nil
}
