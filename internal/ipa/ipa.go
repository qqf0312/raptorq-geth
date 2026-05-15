package ipa

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

const transcriptDomain = "internal/ipa/basic-v1"

// Proof is a basic single inner product proof. This module intentionally does
// not implement batching, aggregation, or any higher-level protocol.
type Proof struct {
	L []bn254.G1Affine
	R []bn254.G1Affine

	AFinal fr.Element
	BFinal fr.Element
}

// Prove proves knowledge of b such that Q = sum_i b_i*G_i and c = <a,b>.
// The implementation uses additive elliptic-curve notation throughout.
func Prove(params *Params, a, b []fr.Element) (*Proof, bn254.G1Affine, fr.Element, error) {
	if len(a) != len(b) {
		return nil, bn254.G1Affine{}, fr.Element{}, fmt.Errorf("vector length mismatch: %d != %d", len(a), len(b))
	}
	if err := validateVectorLen(len(a)); err != nil {
		return nil, bn254.G1Affine{}, fr.Element{}, err
	}
	if err := validateParams(params, len(a)); err != nil {
		return nil, bn254.G1Affine{}, fr.Element{}, err
	}

	Q, err := CommitB(params, b)
	if err != nil {
		return nil, bn254.G1Affine{}, fr.Element{}, err
	}
	c, err := InnerProduct(a, b)
	if err != nil {
		return nil, bn254.G1Affine{}, fr.Element{}, err
	}
	P, err := ComputeP(params, a, Q, c)
	if err != nil {
		return nil, bn254.G1Affine{}, fr.Element{}, err
	}

	aWork := append([]fr.Element(nil), a...)
	bWork := append([]fr.Element(nil), b...)
	gWork := append([]bn254.G1Affine(nil), params.G[:len(a)]...)
	hWork := append([]bn254.G1Affine(nil), params.H[:len(a)]...)

	proof := &Proof{
		L: make([]bn254.G1Affine, 0, log2(len(a))),
		R: make([]bn254.G1Affine, 0, log2(len(a))),
	}

	for round := 0; len(aWork) > 1; round++ {
		aL, aR := splitScalars(aWork)
		bL, bR := splitScalars(bWork)
		gL, gR := splitPoints(gWork)
		hL, hR := splitPoints(hWork)

		L, err := computeCrossTerm(params.U, aL, bR, gL, hR)
		if err != nil {
			return nil, bn254.G1Affine{}, fr.Element{}, err
		}
		R, err := computeCrossTerm(params.U, aR, bL, gR, hL)
		if err != nil {
			return nil, bn254.G1Affine{}, fr.Element{}, err
		}
		proof.L = append(proof.L, L)
		proof.R = append(proof.R, R)

		x, err := ChallengeScalar(transcriptDomain, round, P, L, R)
		if err != nil {
			return nil, bn254.G1Affine{}, fr.Element{}, err
		}
		var xInv, x2, xInv2 fr.Element
		xInv.Inverse(&x)
		x2.Mul(&x, &x)
		xInv2.Mul(&xInv, &xInv)

		// Standard IPA folding:
		// P' = x^2*L + P + x^-2*R.
		// G' = x*G_L + x^-1*G_R, H' = x^-1*H_L + x*H_R.
		// a' = x*a_L + x^-1*a_R, b' = x^-1*b_L + x*b_R.
		P = pointAdd(pointScalarMul(L, x2), P, pointScalarMul(R, xInv2))
		gWork = foldGenerators(gL, gR, x, xInv)
		hWork = foldGenerators(hL, hR, xInv, x)
		aWork = foldScalars(aL, aR, x, xInv)
		bWork = foldScalars(bL, bR, xInv, x)
	}

	proof.AFinal = aWork[0]
	proof.BFinal = bWork[0]
	return proof, Q, c, nil
}

// Verify checks a basic IPA proof. It reconstructs P from public a, Q and c,
// replays the transcript, and verifies the final one-dimensional relation.
func Verify(params *Params, a []fr.Element, Q bn254.G1Affine, c fr.Element, proof *Proof) (bool, error) {
	if proof == nil {
		return false, fmt.Errorf("nil proof")
	}
	if err := validateVectorLen(len(a)); err != nil {
		return false, err
	}
	if err := validateParams(params, len(a)); err != nil {
		return false, err
	}
	rounds := log2(len(a))
	if len(proof.L) != rounds || len(proof.R) != rounds {
		return false, fmt.Errorf("invalid proof rounds: L=%d R=%d want=%d", len(proof.L), len(proof.R), rounds)
	}

	P, err := ComputeP(params, a, Q, c)
	if err != nil {
		return false, err
	}

	aWork := append([]fr.Element(nil), a...)
	gWork := append([]bn254.G1Affine(nil), params.G[:len(a)]...)
	hWork := append([]bn254.G1Affine(nil), params.H[:len(a)]...)

	for round := 0; len(aWork) > 1; round++ {
		aL, aR := splitScalars(aWork)
		gL, gR := splitPoints(gWork)
		hL, hR := splitPoints(hWork)
		L := proof.L[round]
		R := proof.R[round]

		x, err := ChallengeScalar(transcriptDomain, round, P, L, R)
		if err != nil {
			return false, err
		}
		var xInv, x2, xInv2 fr.Element
		xInv.Inverse(&x)
		x2.Mul(&x, &x)
		xInv2.Mul(&xInv, &xInv)

		P = pointAdd(pointScalarMul(L, x2), P, pointScalarMul(R, xInv2))
		gWork = foldGenerators(gL, gR, x, xInv)
		hWork = foldGenerators(hL, hR, xInv, x)
		aWork = foldScalars(aL, aR, x, xInv)
	}

	if !proof.AFinal.Equal(&aWork[0]) {
		return false, nil
	}

	var finalInner fr.Element
	finalInner.Mul(&proof.AFinal, &proof.BFinal)
	right := pointAdd(
		pointScalarMul(hWork[0], proof.AFinal),
		pointScalarMul(gWork[0], proof.BFinal),
		pointScalarMul(params.U, finalInner),
	)
	return pointEqual(P, right), nil
}

func computeCrossTerm(u bn254.G1Affine, aPart, bPart []fr.Element, gPart, hPart []bn254.G1Affine) (bn254.G1Affine, error) {
	aH, err := multiScalarCommit(hPart, aPart)
	if err != nil {
		return bn254.G1Affine{}, err
	}
	bG, err := multiScalarCommit(gPart, bPart)
	if err != nil {
		return bn254.G1Affine{}, err
	}
	inner, err := InnerProduct(aPart, bPart)
	if err != nil {
		return bn254.G1Affine{}, err
	}
	return pointAdd(aH, bG, pointScalarMul(u, inner)), nil
}
