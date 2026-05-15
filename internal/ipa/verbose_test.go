package ipa

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func TestIPAProveVerifyVerboseExample(t *testing.T) {
	params, err := NewTestParams(4)
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	a := []fr.Element{*scalar(1), *scalar(2), *scalar(3), *scalar(4)}
	b := []fr.Element{*scalar(5), *scalar(6), *scalar(7), *scalar(8)}

	c, err := InnerProduct(a, b)
	if err != nil {
		t.Fatalf("inner product: %v", err)
	}
	Q, err := CommitB(params, b)
	if err != nil {
		t.Fatalf("commit b: %v", err)
	}
	P, err := ComputeP(params, a, Q, c)
	if err != nil {
		t.Fatalf("compute P: %v", err)
	}

	proof, QFromProve, cFromProve, err := Prove(params, a, b)
	if err != nil {
		t.Fatalf("prove: %v", err)
	}
	ok, err := Verify(params, a, Q, c, proof)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatal("expected proof to verify")
	}

	t.Log("========== IPA BASIC EXAMPLE ==========")
	t.Logf("  a = %s", scalarSliceToString(a))
	t.Logf("  b = %s", scalarSliceToString(b))
	t.Logf("  c = <a,b> = %s", scalarToString(c))
	t.Logf("  Q = CommitB(b) = %s", pointToString(Q))
	t.Logf("  P = ComputeP(a,Q,c) = %s", pointToString(P))
	for i := range proof.L {
		t.Logf("  proof.L[%d] = %s", i, pointToString(proof.L[i]))
		t.Logf("  proof.R[%d] = %s", i, pointToString(proof.R[i]))
	}
	t.Logf("  proof.AFinal = %s", scalarToString(proof.AFinal))
	t.Logf("  proof.BFinal = %s", scalarToString(proof.BFinal))
	t.Logf("  proof = %s", proofToString(proof))
	t.Logf("  QFromProve = %s", pointToString(QFromProve))
	t.Logf("  cFromProve = %s", scalarToString(cFromProve))
	t.Logf("  verify = %v", ok)
}

func scalarToString(x fr.Element) string {
	var n big.Int
	x.BigInt(&n)
	return n.String()
}

func scalarSliceToString(v []fr.Element) string {
	parts := make([]string, len(v))
	for i := range v {
		parts[i] = scalarToString(v[i])
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pointToString(p bn254.G1Affine) string {
	raw := p.RawBytes()
	return "0x" + hex.EncodeToString(raw[:])
}

func pointSliceToString(v []bn254.G1Affine) string {
	parts := make([]string, len(v))
	for i := range v {
		parts[i] = pointToString(v[i])
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func proofToString(proof *Proof) string {
	if proof == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("{")
	for i := range proof.L {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "L[%d]=%s, R[%d]=%s", i, pointToString(proof.L[i]), i, pointToString(proof.R[i]))
	}
	if len(proof.L) > 0 {
		b.WriteString(", ")
	}
	fmt.Fprintf(&b, "AFinal=%s, BFinal=%s}", scalarToString(proof.AFinal), scalarToString(proof.BFinal))
	return b.String()
}
