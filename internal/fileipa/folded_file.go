package fileipa

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/folding"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

// FoldedFileIPAResult is the shared-witness folded proof for multiple flat file
// IPA rows over the same files. Each coeff set creates one row
// c_j = <A_j, B_flat>. The shared Q = CommitB(B_flat) is supplied by the
// verifier and is not repeated in the rows or folded proof.
type FoldedFileIPAResult struct {
	Params    *ipa.Params
	Rows      []FileIPARow
	FoldProof *FileFoldProof
	IPAProof  *ipa.Proof
}

// FileFoldProof is only a compact transcript record for complete folding. It
// stores Fiat-Shamir challenges, not leaves, roots, Q values, P values, or B.
// The verifier recomputes challenges from rows plus sharedQ; these challenges
// are checked but are not trusted as folding witnesses.
type FileFoldProof struct {
	Challenges [][]fr.Element
}

// BuildFoldedFileIPA builds multiple flat file IPA statements over one shared
// file witness, folds them with the existing shared-witness folding module, and
// proves the folded root with one IPA proof.
func BuildFoldedFileIPA(files [][]byte, coeffSets [][]fr.Element, domain string) (*FoldedFileIPAResult, error) {
	statements, bProof, params, _, _, _, _, _, err := buildFoldedStatements(files, coeffSets)
	if err != nil {
		return nil, err
	}
	root, foldProof, err := folding.FoldStatements(statements, domain)
	if err != nil {
		return nil, fmt.Errorf("fold file IPA statements: %w", err)
	}
	ipaProof, qFromProve, cFromProve, err := ipa.Prove(params, root.A, bProof)
	if err != nil {
		return nil, fmt.Errorf("prove folded file IPA root: %w", err)
	}
	if !qFromProve.Equal(&root.Q) {
		return nil, fmt.Errorf("IPA prover commitment does not match folded root Q")
	}
	if !cFromProve.Equal(&root.C) {
		return nil, fmt.Errorf("IPA prover inner product does not match folded root C")
	}

	return &FoldedFileIPAResult{
		Params:    params,
		Rows:      statementsToRows(statements),
		FoldProof: stripFoldProof(foldProof),
		IPAProof:  ipaProof,
	}, nil
}

// VerifyFoldedFileIPAResult verifies multi-row FileIPA. The prover sends rows
// [(A_j, C_j)], FileFoldProof, and one IPAProof; it does not send B, per-row Q,
// or per-row P. The verifier builds Statement_j = (A_j, sharedQ, C_j), folds
// deterministically with Q carried unchanged, recomputes the folding transcript,
// and verifies one IPA proof for the folded root.
func VerifyFoldedFileIPAResult(result *FoldedFileIPAResult, sharedQ bn254.G1Affine, domain string) (bool, error) {
	if result == nil {
		return false, fmt.Errorf("nil folded file IPA result")
	}
	statements, err := rowsToStatements(result.Rows, sharedQ)
	if err != nil {
		return false, err
	}
	root, localProof, err := folding.FoldStatements(statements, domain)
	if err != nil {
		return false, err
	}
	if result.FoldProof == nil {
		return false, fmt.Errorf("nil file fold proof")
	}
	if !challengeLayersEqual(result.FoldProof.Challenges, localProof.Challenges) {
		return false, nil
	}
	return ipa.Verify(result.Params, root.A, root.Q, root.C, result.IPAProof)
}

func buildFoldedStatements(files [][]byte, coeffSets [][]fr.Element) ([]folding.Statement, []fr.Element, *ipa.Params, bn254.G1Affine, int, int, int, []int, error) {
	if len(coeffSets) == 0 {
		return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("empty coefficient sets")
	}
	if len(coeffSets) == 1 {
		return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("single coefficient set should use FileIPA")
	}
	if !isPowerOfTwo(len(coeffSets)) {
		return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("coefficient set count %d is not a power of two", len(coeffSets))
	}
	chunks, chunkCount, err := filesToFrChunks(coeffSets[0], files)
	if err != nil {
		return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, err
	}
	bFlat := flattenFrChunks(chunks)
	semanticLen := len(bFlat)
	bProof := padScalars(bFlat, nextPowerOfTwo(len(bFlat)))
	params, err := ipa.NewTestParams(len(bProof))
	if err != nil {
		return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("create IPA params: %w", err)
	}
	q, err := ipa.CommitB(params, bProof)
	if err != nil {
		return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("commit shared file witness: %w", err)
	}

	statements := make([]folding.Statement, len(coeffSets))
	for j := range coeffSets {
		if len(coeffSets[j]) != len(files) {
			return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("coeff/file count mismatch for set %d: %d != %d", j, len(coeffSets[j]), len(files))
		}
		aFlat := buildFlatFileCoeffVector(coeffSets[j], chunkCount)
		if len(aFlat) != semanticLen {
			return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("flat vector length mismatch for set %d: %d != %d", j, len(aFlat), semanticLen)
		}
		aProof := padScalars(aFlat, len(bProof))
		c, err := ipa.InnerProduct(aProof, bProof)
		if err != nil {
			return nil, nil, nil, bn254.G1Affine{}, 0, 0, 0, nil, fmt.Errorf("statement %d inner product: %w", j, err)
		}
		statements[j] = folding.Statement{
			A: aProof,
			Q: q,
			C: c,
		}
	}
	return statements, bProof, params, q, semanticLen, len(bProof), chunkCount, fileLengths(files), nil
}

func isPowerOfTwo(n int) bool {
	return n > 0 && n&(n-1) == 0
}

func cloneStatements(in []folding.Statement) []folding.Statement {
	out := make([]folding.Statement, len(in))
	for i := range in {
		out[i] = cloneStatement(in[i])
	}
	return out
}

func statementsToRows(in []folding.Statement) []FileIPARow {
	out := make([]FileIPARow, len(in))
	for i := range in {
		out[i] = FileIPARow{
			A: cloneScalars(in[i].A),
			C: in[i].C,
		}
	}
	return out
}

func rowsToStatements(rows []FileIPARow, sharedQ bn254.G1Affine) ([]folding.Statement, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty rows")
	}
	out := make([]folding.Statement, len(rows))
	for i := range rows {
		if len(rows[i].A) == 0 {
			return nil, fmt.Errorf("row %d has empty vector", i)
		}
		out[i] = folding.Statement{
			A: cloneScalars(rows[i].A),
			Q: sharedQ,
			C: rows[i].C,
		}
	}
	return out, nil
}

func stripFoldProof(proof *folding.FoldProof) *FileFoldProof {
	if proof == nil {
		return nil
	}
	return &FileFoldProof{Challenges: cloneScalarMatrix(proof.Challenges)}
}

func cloneStatement(in folding.Statement) folding.Statement {
	return folding.Statement{
		A: cloneScalars(in.A),
		Q: in.Q,
		C: in.C,
	}
}

func statementEqual(a, b folding.Statement) bool {
	if len(a.A) != len(b.A) {
		return false
	}
	for i := range a.A {
		if !a.A[i].Equal(&b.A[i]) {
			return false
		}
	}
	return a.Q.Equal(&b.Q) && a.C.Equal(&b.C)
}

func statementSlicesEqual(a, b []folding.Statement) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !statementEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func challengeLayersEqual(a, b [][]fr.Element) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !frSlicesEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
