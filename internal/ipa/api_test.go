package ipa

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func TestBuildIPAAndVerifyResult(t *testing.T) {
	params, a, b := testInputs(t, 4)

	result, err := BuildIPA(params, a, b)
	if err != nil {
		t.Fatalf("build IPA: %v", err)
	}
	if result.Params != params {
		t.Fatal("result params differ from input params")
	}
	if len(result.A) != len(a) {
		t.Fatalf("result A length %d != %d", len(result.A), len(a))
	}
	for i := range a {
		if !result.A[i].Equal(&a[i]) {
			t.Fatalf("result A[%d] differs from input", i)
		}
	}

	expectedQ, err := CommitB(params, b)
	if err != nil {
		t.Fatalf("commit b: %v", err)
	}
	if !result.Q.Equal(&expectedQ) {
		t.Fatal("result Q does not match CommitB(b)")
	}
	expectedC, err := InnerProduct(a, b)
	if err != nil {
		t.Fatalf("inner product: %v", err)
	}
	if !result.C.Equal(&expectedC) {
		t.Fatal("result C does not match <a,b>")
	}
	expectedP, err := ComputeP(params, a, result.Q, result.C)
	if err != nil {
		t.Fatalf("compute P: %v", err)
	}
	if !result.P.Equal(&expectedP) {
		t.Fatal("result P does not match ComputeP(a,Q,c)")
	}

	ok, err := VerifyIPAResult(result)
	if err != nil {
		t.Fatalf("verify IPA result: %v", err)
	}
	if !ok {
		t.Fatal("expected IPA result to verify")
	}
}

func TestVerifyIPAResultRejectsTamperedResult(t *testing.T) {
	params, a, b := testInputs(t, 4)
	result, err := BuildIPA(params, a, b)
	if err != nil {
		t.Fatalf("build IPA: %v", err)
	}

	one := scalar(1)
	result.C.Add(&result.C, one)

	ok, err := VerifyIPAResult(result)
	if err != nil {
		t.Fatalf("verify tampered IPA result: %v", err)
	}
	if ok {
		t.Fatal("expected tampered IPA result to be rejected")
	}
}

func TestBuildIPARejectsMismatchedLengths(t *testing.T) {
	params, err := NewTestParams(4)
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	a := []fr.Element{*scalar(1), *scalar(2), *scalar(3), *scalar(4)}
	b := []fr.Element{*scalar(5), *scalar(6), *scalar(7)}

	if _, err := BuildIPA(params, a, b); err == nil {
		t.Fatal("expected BuildIPA to reject mismatched vector lengths")
	}
}
