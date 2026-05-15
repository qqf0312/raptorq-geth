package folding

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

func TestFoldStatementsVerboseExample(t *testing.T) {
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
		t.Fatal("expected folding proof to verify")
	}

	t.Log("========== FOLDING EXAMPLE ==========")
	t.Logf("  shared b = %s", scalarSliceToString(b))
	t.Logf("  shared Q = %s", pointToString(Q))
	for i, stmt := range statements {
		t.Logf("  statement[%d] = %s", i, statementToString(stmt))
	}
	for level, challenges := range foldProof.Challenges {
		for idx, chi := range challenges {
			t.Logf("  challenge[level=%d][index=%d] = %s", level, idx, scalarToString(chi))
		}
	}
	t.Logf("  foldProof = %s", foldProofToString(foldProof))
	t.Logf("  root = %s", statementToString(root))
	t.Logf("  root.A = %s", scalarSliceToString(root.A))
	t.Logf("  root.Q = %s", pointToString(root.Q))
	t.Logf("  root.C = %s", scalarToString(root.C))
	t.Logf("  VerifyFold = %v", ok)
}

func TestFoldedRootCanBeVerifiedByIPAVerboseExample(t *testing.T) {
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
	foldOK, err := VerifyFold(statements, root, foldProof, "ipa-fold-test")
	if err != nil {
		t.Fatalf("verify fold: %v", err)
	}
	if !foldOK {
		t.Fatal("expected folding proof to verify")
	}

	expectedC, err := ipa.InnerProduct(root.A, b)
	if err != nil {
		t.Fatalf("root inner product: %v", err)
	}
	if !expectedC.Equal(&root.C) {
		t.Fatal("root C does not match <root.A,b>")
	}
	if !pointEqual(root.Q, Q) {
		t.Fatal("root Q changed from shared witness commitment")
	}

	ipaProof, QFromProve, cFromProve, err := ipa.Prove(params, root.A, b)
	if err != nil {
		t.Fatalf("ipa prove root: %v", err)
	}
	if !QFromProve.Equal(&root.Q) {
		t.Fatal("IPA prover commitment does not match folded root Q")
	}
	if !cFromProve.Equal(&root.C) {
		t.Fatal("IPA prover inner product does not match folded root C")
	}

	ipaOK, err := ipa.Verify(params, root.A, root.Q, root.C, ipaProof)
	if err != nil {
		t.Fatalf("ipa verify root: %v", err)
	}
	if !ipaOK {
		t.Fatal("expected IPA proof to verify for folded root")
	}

	t.Log("========== FOLDED ROOT IPA EXAMPLE ==========")
	t.Logf("  shared b = %s", scalarSliceToString(b))
	t.Logf("  shared Q = %s", pointToString(Q))
	for i, stmt := range statements {
		t.Logf("  statement[%d] = %s", i, statementToString(stmt))
	}
	for level, challenges := range foldProof.Challenges {
		for idx, chi := range challenges {
			t.Logf("  challenge[level=%d][index=%d] = %s", level, idx, scalarToString(chi))
		}
	}
	t.Logf("  root.A = %s", scalarSliceToString(root.A))
	t.Logf("  root.Q = %s", pointToString(root.Q))
	t.Logf("  root.C = %s", scalarToString(root.C))
	t.Logf("  expectedC = <root.A,b> = %s", scalarToString(expectedC))
	t.Logf("  expectedQ = %s", pointToString(Q))
	t.Logf("  ipaProof = %s", proofToString(ipaProof))
	for i := range ipaProof.L {
		t.Logf("  ipaProof.L[%d] = %s", i, pointToString(ipaProof.L[i]))
		t.Logf("  ipaProof.R[%d] = %s", i, pointToString(ipaProof.R[i]))
	}
	t.Logf("  ipaProof.AFinal = %s", scalarToString(ipaProof.AFinal))
	t.Logf("  ipaProof.BFinal = %s", scalarToString(ipaProof.BFinal))
	t.Logf("  QFromProve = %s", pointToString(QFromProve))
	t.Logf("  cFromProve = %s", scalarToString(cFromProve))
	t.Logf("  IPA Verify = %v", ipaOK)
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

func proofToString(proof *ipa.Proof) string {
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

func statementToString(stmt Statement) string {
	return fmt.Sprintf("{A=%s, Q=%s, C=%s}", scalarSliceToString(stmt.A), pointToString(stmt.Q), scalarToString(stmt.C))
}

func foldProofToString(proof *FoldProof) string {
	if proof == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("{")
	for level, challenges := range proof.Challenges {
		if level > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "level[%d]=[", level)
		for idx, chi := range challenges {
			if idx > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "step[%d]=%s", idx, scalarToString(chi))
		}
		b.WriteString("]")
	}
	fmt.Fprintf(&b, ", root=%s}", statementToString(proof.Root.Statement))
	return b.String()
}
