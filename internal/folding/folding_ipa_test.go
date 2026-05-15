package folding

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

func TestFoldedRootCanBeVerifiedByIPA(t *testing.T) {
	params, err := ipa.NewTestParams(4)
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	b := []fr.Element{fe(5), fe(6), fe(7), fe(8)}
	Q, err := ipa.CommitB(params, b)
	if err != nil {
		t.Fatalf("commit shared witness: %v", err)
	}

	rows := [][]fr.Element{
		{fe(1), fe(2), fe(3), fe(4)},
		{fe(2), fe(3), fe(4), fe(5)},
		{fe(3), fe(4), fe(5), fe(6)},
		{fe(4), fe(5), fe(6), fe(7)},
	}
	statements := makeSharedWitnessStatements(t, rows, b, Q)

	root, foldProof, err := FoldStatements(statements, "ipa-fold-test")
	if err != nil {
		t.Fatalf("fold statements: %v", err)
	}
	ok, err := VerifyFold(statements, root, foldProof, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify fold: %v", err)
	}
	if !ok {
		t.Fatal("fold verification failed")
	}
	if !pointEqual(root.Q, Q) {
		t.Fatal("root Q changed, expected shared commitment to remain unchanged")
	}

	expectedC, err := ipa.InnerProduct(root.A, b)
	if err != nil {
		t.Fatalf("root inner product: %v", err)
	}
	if !expectedC.Equal(&root.C) {
		t.Fatal("root C does not match <root.A,b>")
	}

	proof, QFromProve, cFromProve, err := ipa.Prove(params, root.A, b)
	if err != nil {
		t.Fatalf("ipa prove root: %v", err)
	}
	if !QFromProve.Equal(&root.Q) {
		t.Fatal("IPA prover commitment does not match folded root Q")
	}
	if !cFromProve.Equal(&root.C) {
		t.Fatal("IPA prover inner product does not match folded root C")
	}

	ok, err = ipa.Verify(params, root.A, root.Q, root.C, proof)
	if err != nil {
		t.Fatalf("ipa verify root: %v", err)
	}
	if !ok {
		t.Fatal("IPA verify failed for folded root statement")
	}
}

func TestSharedWitnessFoldingIPAIntegration(t *testing.T) {
	params, err := ipa.NewTestParams(4)
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	b := []fr.Element{fe(13), fe(21), fe(34), fe(55)}
	Q, err := ipa.CommitB(params, b)
	if err != nil {
		t.Fatalf("commit shared witness: %v", err)
	}

	rows := [][]fr.Element{
		{fe(1), fe(0), fe(2), fe(3)},
		{fe(5), fe(8), fe(13), fe(21)},
		{fe(34), fe(3), fe(2), fe(1)},
		{fe(7), fe(11), fe(17), fe(19)},
	}
	statements := makeSharedWitnessStatements(t, rows, b, Q)
	for i := range statements {
		if !pointEqual(statements[i].Q, Q) {
			t.Fatalf("statement %d does not share Q", i)
		}
		c, err := ipa.InnerProduct(statements[i].A, b)
		if err != nil {
			t.Fatalf("statement %d inner product: %v", i, err)
		}
		if !c.Equal(&statements[i].C) {
			t.Fatalf("statement %d C does not match <A,b>", i)
		}
	}

	root, foldProof, err := FoldStatements(statements, "ipa-fold-shared-witness-integration")
	if err != nil {
		t.Fatalf("fold statements: %v", err)
	}
	ok, err := VerifyFold(statements, root, foldProof, "ipa-fold-shared-witness-integration")
	if err != nil {
		t.Fatalf("verify fold: %v", err)
	}
	if !ok {
		t.Fatal("expected fold proof to verify")
	}
	if !pointEqual(root.Q, Q) {
		t.Fatal("root Q changed from shared witness commitment")
	}

	expectedRootC, err := ipa.InnerProduct(root.A, b)
	if err != nil {
		t.Fatalf("root inner product: %v", err)
	}
	if !expectedRootC.Equal(&root.C) {
		t.Fatal("root C does not match <root.A,b>")
	}

	proof, QFromProver, cFromProver, err := ipa.Prove(params, root.A, b)
	if err != nil {
		t.Fatalf("ipa prove folded root: %v", err)
	}
	if !pointEqual(QFromProver, root.Q) {
		t.Fatal("IPA prover Q does not match folded root Q")
	}
	if !cFromProver.Equal(&root.C) {
		t.Fatal("IPA prover C does not match folded root C")
	}

	ok, err = ipa.Verify(params, root.A, root.Q, root.C, proof)
	if err != nil {
		t.Fatalf("ipa verify folded root: %v", err)
	}
	if !ok {
		t.Fatal("expected IPA proof to verify for folded root")
	}

	tampered := cloneStatements(statements)
	one := fe(1)
	tampered[2].C.Add(&tampered[2].C, &one)
	tamperedRoot, _, err := FoldStatements(tampered, "ipa-fold-shared-witness-integration")
	if err != nil {
		t.Fatalf("fold tampered statements: %v", err)
	}
	expectedTamperedC, err := ipa.InnerProduct(tamperedRoot.A, b)
	if err != nil {
		t.Fatalf("tampered root inner product: %v", err)
	}
	if expectedTamperedC.Equal(&tamperedRoot.C) {
		t.Fatal("tampered row unexpectedly preserved root relation")
	}

	mismatchedQ := cloneStatements(statements)
	mismatchedB := []fr.Element{fe(55), fe(34), fe(21), fe(13)}
	mismatchedQ[1].Q, err = ipa.CommitB(params, mismatchedB)
	if err != nil {
		t.Fatalf("commit mismatched witness: %v", err)
	}
	if _, _, err := FoldStatements(mismatchedQ, "ipa-fold-shared-witness-integration"); err == nil {
		t.Fatal("expected folding to reject mismatched Q")
	}
}

func TestFoldedRootRejectsMismatchedQ(t *testing.T) {
	params, err := ipa.NewTestParams(4)
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	b1 := []fr.Element{fe(5), fe(6), fe(7), fe(8)}
	b2 := []fr.Element{fe(8), fe(7), fe(6), fe(5)}
	Q1, err := ipa.CommitB(params, b1)
	if err != nil {
		t.Fatalf("commit b1: %v", err)
	}
	Q2, err := ipa.CommitB(params, b2)
	if err != nil {
		t.Fatalf("commit b2: %v", err)
	}

	left := Statement{A: []fr.Element{fe(1), fe(2), fe(3), fe(4)}, Q: Q1, C: fe(10)}
	right := Statement{A: []fr.Element{fe(2), fe(3), fe(4), fe(5)}, Q: Q2, C: fe(20)}

	if _, _, err := FoldTwo(left, right, "ipa-fold-test", 0); err == nil {
		t.Fatal("expected FoldTwo to reject mismatched Q")
	}
	if _, _, err := FoldStatements([]Statement{left, right}, "ipa-fold-test"); err == nil {
		t.Fatal("expected FoldStatements to reject mismatched Q")
	}
}

func TestFoldedRootIPARejectsTamperedRowC(t *testing.T) {
	params, err := ipa.NewTestParams(4)
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	b := []fr.Element{fe(5), fe(6), fe(7), fe(8)}
	Q, err := ipa.CommitB(params, b)
	if err != nil {
		t.Fatalf("commit shared witness: %v", err)
	}

	rows := [][]fr.Element{
		{fe(1), fe(2), fe(3), fe(4)},
		{fe(2), fe(3), fe(4), fe(5)},
		{fe(3), fe(4), fe(5), fe(6)},
		{fe(4), fe(5), fe(6), fe(7)},
	}
	statements := makeSharedWitnessStatements(t, rows, b, Q)
	one := fe(1)
	statements[1].C.Add(&statements[1].C, &one)

	root, _, err := FoldStatements(statements, "ipa-fold-test")
	if err != nil {
		t.Fatalf("fold tampered statements: %v", err)
	}
	expectedC, err := ipa.InnerProduct(root.A, b)
	if err != nil {
		t.Fatalf("root inner product: %v", err)
	}
	if expectedC.Equal(&root.C) {
		t.Fatal("tampered row C unexpectedly produced a valid root C")
	}

	proof, _, _, err := ipa.Prove(params, root.A, b)
	if err != nil {
		t.Fatalf("ipa prove root with real witness: %v", err)
	}
	ok, err := ipa.Verify(params, root.A, root.Q, root.C, proof)
	if err != nil {
		t.Fatalf("ipa verify tampered root: %v", err)
	}
	if ok {
		t.Fatal("expected IPA verification to reject tampered folded root C")
	}
}

func makeSharedWitnessStatements(t *testing.T, rows [][]fr.Element, b []fr.Element, Q bn254.G1Affine) []Statement {
	t.Helper()
	statements := make([]Statement, len(rows))
	for i := range rows {
		c, err := ipa.InnerProduct(rows[i], b)
		if err != nil {
			t.Fatalf("row %d inner product: %v", i, err)
		}
		statements[i] = Statement{
			A: append([]fr.Element(nil), rows[i]...),
			Q: Q,
			C: c,
		}
	}
	return statements
}
