package ipa

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// IPAResult contains the public statement and proof produced by BuildIPA.
// A is copied from the caller input, Q is CommitB(b), C is <A,b>, P is
// ComputeP(A,Q,C), and Proof is the basic IPA proof for the relation.
type IPAResult struct {
	Params *Params
	A      []fr.Element
	Q      bn254.G1Affine
	C      fr.Element
	P      bn254.G1Affine
	Proof  *Proof
}

// BuildIPA computes Q = CommitB(b), C = <a,b>, P = CommitA(a)+Q+C*U and a
// basic IPA proof. The supplied params define the public generators used by
// both proving and verification.
func BuildIPA(params *Params, a, b []fr.Element) (*IPAResult, error) {
	if len(a) != len(b) {
		return nil, fmt.Errorf("vector length mismatch: %d != %d", len(a), len(b))
	}
	if err := validateVectorLen(len(a)); err != nil {
		return nil, err
	}
	if err := validateParams(params, len(a)); err != nil {
		return nil, err
	}

	Q, err := CommitB(params, b)
	if err != nil {
		return nil, err
	}
	c, err := InnerProduct(a, b)
	if err != nil {
		return nil, err
	}
	P, err := ComputeP(params, a, Q, c)
	if err != nil {
		return nil, err
	}
	proof, qFromProve, cFromProve, err := Prove(params, a, b)
	if err != nil {
		return nil, err
	}
	if !qFromProve.Equal(&Q) {
		return nil, fmt.Errorf("prover commitment does not match computed commitment")
	}
	if !cFromProve.Equal(&c) {
		return nil, fmt.Errorf("prover inner product does not match computed inner product")
	}

	return &IPAResult{
		Params: params,
		A:      append([]fr.Element(nil), a...),
		Q:      Q,
		C:      c,
		P:      P,
		Proof:  proof,
	}, nil
}

// VerifyIPAResult verifies an IPAResult using the basic IPA verifier. It also
// checks that the stored P matches ComputeP(A,Q,C), so the result is internally
// consistent with the public statement it carries.
func VerifyIPAResult(result *IPAResult) (bool, error) {
	if result == nil {
		return false, fmt.Errorf("nil IPA result")
	}
	P, err := ComputeP(result.Params, result.A, result.Q, result.C)
	if err != nil {
		return false, err
	}
	if !P.Equal(&result.P) {
		return false, nil
	}
	return Verify(result.Params, result.A, result.Q, result.C, result.Proof)
}
