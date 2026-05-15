package folding

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

func TestBuildFoldedIPAAndVerifyResult(t *testing.T) {
	params, err := ipa.NewTestParams(4)
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	b := []fr.Element{fe(5), fe(6), fe(7), fe(8)}
	vectors := [][]fr.Element{
		{fe(1), fe(2), fe(3), fe(4)},
		{fe(2), fe(3), fe(4), fe(5)},
		{fe(3), fe(4), fe(5), fe(6)},
		{fe(4), fe(5), fe(6), fe(7)},
	}

	result, err := BuildFoldedIPA(params, vectors, b, "ipa-fold-test")
	if err != nil {
		t.Fatalf("build folded IPA: %v", err)
	}
	if len(result.Leaves) != len(vectors) {
		t.Fatalf("leaf count %d != %d", len(result.Leaves), len(vectors))
	}
	if !pointEqual(result.Root.Q, result.Q) {
		t.Fatal("root Q differs from shared Q")
	}
	for i := range result.Leaves {
		if !pointEqual(result.Leaves[i].Q, result.Q) {
			t.Fatalf("leaf %d does not share Q", i)
		}
		expectedC, err := ipa.InnerProduct(vectors[i], b)
		if err != nil {
			t.Fatalf("leaf %d inner product: %v", i, err)
		}
		if !result.Leaves[i].C.Equal(&expectedC) {
			t.Fatalf("leaf %d C does not match <A,b>", i)
		}
	}
	expectedRootC, err := ipa.InnerProduct(result.Root.A, b)
	if err != nil {
		t.Fatalf("root inner product: %v", err)
	}
	if !result.Root.C.Equal(&expectedRootC) {
		t.Fatal("root C does not match <root.A,b>")
	}

	ok, err := VerifyFoldedIPAResult(result, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify folded IPA result: %v", err)
	}
	if !ok {
		t.Fatal("expected folded IPA result to verify")
	}
}

func TestVerifyFoldedIPAResultRejectsTamperedRoot(t *testing.T) {
	params, err := ipa.NewTestParams(4)
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	b := []fr.Element{fe(5), fe(6), fe(7), fe(8)}
	vectors := [][]fr.Element{
		{fe(1), fe(2), fe(3), fe(4)},
		{fe(2), fe(3), fe(4), fe(5)},
		{fe(3), fe(4), fe(5), fe(6)},
		{fe(4), fe(5), fe(6), fe(7)},
	}
	result, err := BuildFoldedIPA(params, vectors, b, "ipa-fold-test")
	if err != nil {
		t.Fatalf("build folded IPA: %v", err)
	}

	one := fe(1)
	result.Root.C.Add(&result.Root.C, &one)

	ok, err := VerifyFoldedIPAResult(result, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify tampered folded IPA result: %v", err)
	}
	if ok {
		t.Fatal("expected tampered folded IPA result to be rejected")
	}
}

func TestBuildFoldedIPARejectsMismatchedVectorLength(t *testing.T) {
	params, err := ipa.NewTestParams(4)
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	b := []fr.Element{fe(5), fe(6), fe(7), fe(8)}
	vectors := [][]fr.Element{
		{fe(1), fe(2), fe(3), fe(4)},
		{fe(2), fe(3), fe(4)},
	}

	if _, err := BuildFoldedIPA(params, vectors, b, "ipa-fold-test"); err == nil {
		t.Fatal("expected BuildFoldedIPA to reject mismatched vector length")
	}
}
