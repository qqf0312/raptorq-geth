package folding

import (
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func TestFoldTwo(t *testing.T) {
	q := testPoint(11)
	left := Statement{
		A: []fr.Element{fe(1), fe(2), fe(3), fe(4)},
		Q: q,
		C: fe(10),
	}
	right := Statement{
		A: []fr.Element{fe(5), fe(6), fe(7), fe(8)},
		Q: q,
		C: fe(20),
	}

	folded, chi, err := FoldTwo(left, right, "ipa-fold-test", 0)
	if err != nil {
		t.Fatalf("fold two: %v", err)
	}

	expected := Statement{
		A: foldVector(left.A, right.A, chi),
		Q: q,
		C: addScalars(left.C, scalarMul(right.C, chi)),
	}
	if !statementEqual(folded, expected) {
		t.Fatal("folded statement does not match manual calculation")
	}
}

func TestFoldStatements(t *testing.T) {
	statements := testStatements()
	root, proof, err := FoldStatements(statements, "ipa-fold-test")
	if err != nil {
		t.Fatalf("fold statements: %v", err)
	}

	ok, err := VerifyFold(statements, root, proof, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify fold: %v", err)
	}
	if !ok {
		t.Fatal("expected folding proof to verify")
	}
}

func TestVerifyFoldRejectTamperedRoot(t *testing.T) {
	statements := testStatements()
	root, proof, err := FoldStatements(statements, "ipa-fold-test")
	if err != nil {
		t.Fatalf("fold statements: %v", err)
	}
	one := fe(1)
	root.C.Add(&root.C, &one)

	ok, err := VerifyFold(statements, root, proof, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify fold: %v", err)
	}
	if ok {
		t.Fatal("expected tampered root to be rejected")
	}
}

func TestVerifyFoldRejectTamperedStatement(t *testing.T) {
	statements := testStatements()
	root, proof, err := FoldStatements(statements, "ipa-fold-test")
	if err != nil {
		t.Fatalf("fold statements: %v", err)
	}

	tampered := cloneStatements(statements)
	one := fe(1)
	tampered[2].A[1].Add(&tampered[2].A[1], &one)

	ok, err := VerifyFold(tampered, root, proof, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify fold: %v", err)
	}
	if ok {
		t.Fatal("expected tampered statement A to be rejected")
	}

	tampered = cloneStatements(statements)
	tampered[1].C.Add(&tampered[1].C, &one)
	ok, err = VerifyFold(tampered, root, proof, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify fold with tampered C: %v", err)
	}
	if ok {
		t.Fatal("expected tampered statement C to be rejected")
	}
}

func TestFoldStatementsInvalidLength(t *testing.T) {
	statements := testStatements()[:3]
	if _, _, err := FoldStatements(statements, "ipa-fold-test"); err == nil {
		t.Fatal("expected error for non-power-of-two statement count")
	}
}

func TestFoldStatementsMismatchedVectorLength(t *testing.T) {
	statements := testStatements()
	statements[3].A = statements[3].A[:3]

	if _, _, err := FoldStatements(statements, "ipa-fold-test"); err == nil {
		t.Fatal("expected error for mismatched vector length")
	}
}

func TestFoldDoesNotMutateInput(t *testing.T) {
	statements := testStatements()
	before := cloneStatements(statements)

	if _, _, err := FoldStatements(statements, "ipa-fold-test"); err != nil {
		t.Fatalf("fold statements: %v", err)
	}
	if !statementSlicesEqual(statements, before) {
		t.Fatal("FoldStatements mutated input statements")
	}
}

func testStatements() []Statement {
	q := testPoint(10)
	return []Statement{
		{A: []fr.Element{fe(1), fe(2), fe(3), fe(4)}, Q: q, C: fe(11)},
		{A: []fr.Element{fe(5), fe(6), fe(7), fe(8)}, Q: q, C: fe(22)},
		{A: []fr.Element{fe(9), fe(10), fe(11), fe(12)}, Q: q, C: fe(33)},
		{A: []fr.Element{fe(13), fe(14), fe(15), fe(16)}, Q: q, C: fe(44)},
	}
}

func cloneStatements(statements []Statement) []Statement {
	out := make([]Statement, len(statements))
	for i := range statements {
		out[i] = cloneStatement(statements[i])
	}
	return out
}

func fe(v uint64) fr.Element {
	var out fr.Element
	out.SetUint64(v)
	return out
}

func testPoint(v uint64) bn254.G1Affine {
	_, _, gen, _ := bn254.Generators()
	scalarElement := fe(v)
	var scalar big.Int
	scalarElement.BigInt(&scalar)
	var out bn254.G1Affine
	out.ScalarMultiplication(&gen, &scalar)
	return out
}
