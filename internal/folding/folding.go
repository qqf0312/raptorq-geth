package folding

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// Statement is the public input folded by this package. Each statement matches
// the basic IPA relation Q = Com(b), C = <A,b>. P is intentionally omitted
// because a verifier can rebuild it from A, Q and C in the IPA layer.
type Statement struct {
	A []fr.Element
	Q bn254.G1Affine
	C fr.Element
}

// FoldProof is a complete folding proof for this initial implementation.
// It stores all leaves and every Fiat-Shamir challenge so tests and callers can
// inspect the folding tree. VerifyFold still recomputes all challenges from the
// leaf statements and does not implement selective verification or Merkle paths.
type FoldProof struct {
	Leaves     []Statement
	Challenges [][]fr.Element
	Root       *FoldNode
}

// FoldNode records one node in the folding tree. Internal nodes carry the
// challenge used to fold Left and Right into Statement.
type FoldNode struct {
	Statement Statement
	Left      *FoldNode
	Right     *FoldNode
	Challenge fr.Element
}

// FoldTwo folds two shared-witness statements into one. In this folding mode
// all rows must share the same Q = CommitB(b), so Q is checked for equality and
// carried unchanged. The code uses additive notation for the scalar parts:
// A* = A1 + chi*A2, C* = C1 + chi*C2, Q* = Q1.
func FoldTwo(left, right Statement, domain string, round int) (Statement, fr.Element, error) {
	if len(left.A) == 0 || len(right.A) == 0 {
		return Statement{}, fr.Element{}, errors.New("empty statement vector")
	}
	if len(left.A) != len(right.A) {
		return Statement{}, fr.Element{}, fmt.Errorf("statement vector length mismatch: %d != %d", len(left.A), len(right.A))
	}
	if !pointEqual(left.Q, right.Q) {
		return Statement{}, fr.Element{}, errors.New("statement commitments differ")
	}

	chi, err := ChallengeFoldScalar(domain, round, left, right)
	if err != nil {
		return Statement{}, fr.Element{}, err
	}
	folded := Statement{
		A: foldVector(left.A, right.A, chi),
		Q: left.Q,
		C: addScalars(left.C, scalarMul(right.C, chi)),
	}
	return folded, chi, nil
}

// FoldStatements folds a power-of-two number of statements into a single root
// statement. This package only handles folding; the resulting root can later be
// passed to the basic IPA module for proving or verification.
func FoldStatements(statements []Statement, domain string) (Statement, *FoldProof, error) {
	if len(statements) == 0 {
		return Statement{}, nil, errors.New("empty statements")
	}
	if !isPowerOfTwo(len(statements)) {
		return Statement{}, nil, fmt.Errorf("statement count %d is not a power of two", len(statements))
	}
	if err := validateStatements(statements); err != nil {
		return Statement{}, nil, err
	}

	nodes := make([]*FoldNode, len(statements))
	leaves := make([]Statement, len(statements))
	for i := range statements {
		leaves[i] = cloneStatement(statements[i])
		nodes[i] = &FoldNode{Statement: cloneStatement(statements[i])}
	}

	proof := &FoldProof{
		Leaves:     leaves,
		Challenges: make([][]fr.Element, 0, log2(len(statements))),
	}

	for round := 0; len(nodes) > 1; round++ {
		next := make([]*FoldNode, 0, len(nodes)/2)
		challenges := make([]fr.Element, 0, len(nodes)/2)
		for i := 0; i < len(nodes); i += 2 {
			folded, chi, err := FoldTwo(nodes[i].Statement, nodes[i+1].Statement, domain, round)
			if err != nil {
				return Statement{}, nil, err
			}
			next = append(next, &FoldNode{
				Statement: cloneStatement(folded),
				Left:      nodes[i],
				Right:     nodes[i+1],
				Challenge: chi,
			})
			challenges = append(challenges, chi)
		}
		proof.Challenges = append(proof.Challenges, challenges)
		nodes = next
	}

	proof.Root = nodes[0]
	return cloneStatement(nodes[0].Statement), proof, nil
}

// VerifyFold recomputes the complete folding process from the leaves and checks
// that the local root equals the supplied root. This version does not implement
// selective verification or Merkle-path proofs.
func VerifyFold(statements []Statement, root Statement, proof *FoldProof, domain string) (bool, error) {
	if proof == nil {
		return false, errors.New("nil fold proof")
	}
	localRoot, localProof, err := FoldStatements(statements, domain)
	if err != nil {
		return false, err
	}
	if !statementSlicesEqual(proof.Leaves, statements) {
		return false, nil
	}
	if !challengeLayersEqual(proof.Challenges, localProof.Challenges) {
		return false, nil
	}
	return statementEqual(localRoot, root), nil
}

// ChallengeFoldScalar derives a deterministic Fiat-Shamir scalar from both
// child statements. Prover and verifier must use exactly the same domain and
// round numbering.
func ChallengeFoldScalar(domain string, round int, left, right Statement) (fr.Element, error) {
	if round < 0 {
		return fr.Element{}, errors.New("negative round")
	}
	for counter := uint32(0); ; counter++ {
		h := sha256.New()
		h.Write([]byte(domain))
		writeUint64(h, uint64(round))
		serializeStatement(h, left)
		serializeStatement(h, right)
		writeUint64(h, uint64(counter))

		var chi fr.Element
		chi.SetBytes(h.Sum(nil))
		if !chi.IsZero() {
			return chi, nil
		}
		if counter == ^uint32(0) {
			return fr.Element{}, errors.New("failed to derive non-zero folding challenge")
		}
	}
}

func validateStatements(statements []Statement) error {
	n := len(statements[0].A)
	if n == 0 {
		return errors.New("empty statement vector")
	}
	for i := range statements {
		if len(statements[i].A) == 0 {
			return fmt.Errorf("statement %d has empty vector", i)
		}
		if len(statements[i].A) != n {
			return fmt.Errorf("statement %d vector length %d != %d", i, len(statements[i].A), n)
		}
	}
	return nil
}

func addScalars(a, b fr.Element) fr.Element {
	var out fr.Element
	out.Add(&a, &b)
	return out
}

func scalarMul(a, b fr.Element) fr.Element {
	var out fr.Element
	out.Mul(&a, &b)
	return out
}

func foldVector(left, right []fr.Element, chi fr.Element) []fr.Element {
	out := make([]fr.Element, len(left))
	for i := range left {
		var scaled fr.Element
		scaled.Mul(&right[i], &chi)
		out[i].Add(&left[i], &scaled)
	}
	return out
}

func statementEqual(a, b Statement) bool {
	if len(a.A) != len(b.A) {
		return false
	}
	for i := range a.A {
		if !a.A[i].Equal(&b.A[i]) {
			return false
		}
	}
	return pointEqual(a.Q, b.Q) && a.C.Equal(&b.C)
}

func statementSlicesEqual(a, b []Statement) bool {
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
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if !a[i][j].Equal(&b[i][j]) {
				return false
			}
		}
	}
	return true
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func serializeStatement(w hashWriter, statement Statement) {
	writeUint64(w, uint64(len(statement.A)))
	for i := range statement.A {
		bytes := statement.A[i].Bytes()
		w.Write(bytes[:])
	}
	rawQ := statement.Q.RawBytes()
	w.Write(rawQ[:])
	cBytes := statement.C.Bytes()
	w.Write(cBytes[:])
}

func cloneStatement(statement Statement) Statement {
	cloned := Statement{
		A: make([]fr.Element, len(statement.A)),
		Q: statement.Q,
		C: statement.C,
	}
	copy(cloned.A, statement.A)
	return cloned
}

func pointEqual(a, b bn254.G1Affine) bool {
	return a.Equal(&b)
}

func pointAdd(points ...bn254.G1Affine) bn254.G1Affine {
	var acc bn254.G1Jac
	for i := range points {
		acc.AddMixed(&points[i])
	}
	var out bn254.G1Affine
	out.FromJacobian(&acc)
	return out
}

func pointScalarMul(p bn254.G1Affine, s fr.Element) bn254.G1Affine {
	var scalar big.Int
	s.BigInt(&scalar)
	var out bn254.G1Affine
	out.ScalarMultiplication(&p, &scalar)
	return out
}

func isPowerOfTwo(n int) bool {
	return n > 0 && n&(n-1) == 0
}

func log2(n int) int {
	out := 0
	for n > 1 {
		n >>= 1
		out++
	}
	return out
}

func writeUint64(w hashWriter, v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	w.Write(buf[:])
}
