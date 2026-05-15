package ipa

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func TestIPAProveVerify(t *testing.T) {
	params, a, b := testInputs(t, 4)

	proof, Q, c, err := Prove(params, a, b)
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
}

func TestIPARejectWrongC(t *testing.T) {
	params, a, b := testInputs(t, 4)
	proof, Q, c, err := Prove(params, a, b)
	if err != nil {
		t.Fatalf("prove: %v", err)
	}
	c.Add(&c, scalar(1))

	ok, err := Verify(params, a, Q, c, proof)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Fatal("expected wrong c to be rejected")
	}
}

func TestIPARejectWrongA(t *testing.T) {
	params, a, b := testInputs(t, 4)
	proof, Q, c, err := Prove(params, a, b)
	if err != nil {
		t.Fatalf("prove: %v", err)
	}
	a[1].Add(&a[1], scalar(1))

	ok, err := Verify(params, a, Q, c, proof)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Fatal("expected wrong a to be rejected")
	}
}

func TestIPARejectWrongQ(t *testing.T) {
	params, a, b := testInputs(t, 4)
	proof, _, c, err := Prove(params, a, b)
	if err != nil {
		t.Fatalf("prove: %v", err)
	}
	b2 := []fr.Element{*scalar(9), *scalar(10), *scalar(11), *scalar(12)}
	Q2, err := CommitB(params, b2)
	if err != nil {
		t.Fatalf("commit b2: %v", err)
	}

	ok, err := Verify(params, a, Q2, c, proof)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Fatal("expected wrong Q to be rejected")
	}
}

func TestIPAInvalidLength(t *testing.T) {
	params, err := NewTestParams(4)
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	a := []fr.Element{*scalar(1), *scalar(2), *scalar(3)}
	b := []fr.Element{*scalar(4), *scalar(5), *scalar(6)}

	if _, _, _, err := Prove(params, a, b); err == nil {
		t.Fatal("expected invalid length error")
	}
}

func TestIPARejectTamperedProof(t *testing.T) {
	params, a, b := testInputs(t, 4)
	proof, Q, c, err := Prove(params, a, b)
	if err != nil {
		t.Fatalf("prove: %v", err)
	}
	proof.BFinal.Add(&proof.BFinal, scalar(1))

	ok, err := Verify(params, a, Q, c, proof)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Fatal("expected tampered BFinal to be rejected")
	}

	proof, Q, c, err = Prove(params, a, b)
	if err != nil {
		t.Fatalf("prove again: %v", err)
	}
	proof.L[0] = pointAdd(proof.L[0], baseG1())

	ok, err = Verify(params, a, Q, c, proof)
	if err != nil {
		t.Fatalf("verify tampered L: %v", err)
	}
	if ok {
		t.Fatal("expected tampered L to be rejected")
	}
}

func testInputs(t *testing.T, n int) (*Params, []fr.Element, []fr.Element) {
	t.Helper()
	params, err := NewTestParams(n)
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	a := []fr.Element{*scalar(1), *scalar(2), *scalar(3), *scalar(4)}
	b := []fr.Element{*scalar(5), *scalar(6), *scalar(7), *scalar(8)}
	return params, a, b
}

func scalar(v uint64) *fr.Element {
	var out fr.Element
	out.SetUint64(v)
	return &out
}

func _ensurePointType(_ bn254.G1Affine) {}
