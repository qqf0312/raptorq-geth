package linearrecovery

import (
	"errors"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/fileipa"
)

var ErrSingleNodeNotRecoverable = errors.New("target MPT node is not linearly recoverable")

// LinearEncodedRow is one verified linear encoding row c = <A,B>.
//
// This file assumes the encoded rows have already been verified by the proof
// layer. It only checks linear recoverability and reconstructs one target node
// over bn254/fr.
type LinearEncodedRow struct {
	A []fr.Element
	C fr.Element
}

// RecoveredNode carries a decoded target node together with the intermediate
// values needed to audit the linear recovery.
type RecoveredNode[T any] struct {
	Node   T
	Bytes  []byte
	Value  fr.Element
	Lambda []fr.Element
}

// RowsFromMatrixAndResults converts an A matrix and C vector into rows.
// The matrix is interpreted row-wise: C[i] = <A[i], B>.
func RowsFromMatrixAndResults(a [][]fr.Element, c []fr.Element) ([]LinearEncodedRow, error) {
	if len(a) == 0 {
		return nil, errors.New("empty A matrix")
	}
	if len(a) != len(c) {
		return nil, fmt.Errorf("A row count %d does not match C length %d", len(a), len(c))
	}
	nodeCount := len(a[0])
	if nodeCount == 0 {
		return nil, errors.New("empty A row")
	}

	rows := make([]LinearEncodedRow, len(a))
	for i := range a {
		if len(a[i]) != nodeCount {
			return nil, fmt.Errorf("A row %d length %d does not match %d", i, len(a[i]), nodeCount)
		}
		rows[i] = LinearEncodedRow{
			A: append([]fr.Element(nil), a[i]...),
			C: c[i],
		}
	}
	return rows, nil
}

// RecoverSingleNodeFromMatrix reconstructs one target source-vector element
// from an A matrix and C vector. The node count is inferred from A's columns.
func RecoverSingleNodeFromMatrix(a [][]fr.Element, c []fr.Element, targetIndex int) (fr.Element, []fr.Element, error) {
	rows, err := RowsFromMatrixAndResults(a, c)
	if err != nil {
		return fr.Element{}, nil, err
	}
	return RecoverSingleNode(rows, len(a[0]), targetIndex)
}

// RecoverSingleNodeBytesFromMatrix reconstructs one target source-vector
// element and converts it back to its canonical byte representation.
func RecoverSingleNodeBytesFromMatrix(a [][]fr.Element, c []fr.Element, targetIndex int, byteLen int) ([]byte, fr.Element, []fr.Element, error) {
	if byteLen < 0 {
		return nil, fr.Element{}, nil, fmt.Errorf("negative byteLen %d", byteLen)
	}
	recovered, lambda, err := RecoverSingleNodeFromMatrix(a, c, targetIndex)
	if err != nil {
		return nil, fr.Element{}, nil, err
	}
	bytes, err := fileipa.FrChunksToBytes([]fr.Element{recovered}, byteLen)
	if err != nil {
		return nil, fr.Element{}, nil, fmt.Errorf("convert recovered node fr to bytes: %w", err)
	}
	return bytes, recovered, lambda, nil
}

// RecoverSingleDecodedNodeFromMatrix reconstructs one target source-vector
// element, converts it to bytes, and lets the caller decode those bytes into
// the concrete node type used by the surrounding MPT layer.
func RecoverSingleDecodedNodeFromMatrix[T any](a [][]fr.Element, c []fr.Element, targetIndex int, byteLen int, decode func([]byte) (T, error)) (RecoveredNode[T], error) {
	if decode == nil {
		return RecoveredNode[T]{}, errors.New("nil node decoder")
	}
	bytes, recovered, lambda, err := RecoverSingleNodeBytesFromMatrix(a, c, targetIndex, byteLen)
	if err != nil {
		return RecoveredNode[T]{}, err
	}
	node, err := decode(bytes)
	if err != nil {
		return RecoveredNode[T]{}, fmt.Errorf("decode recovered node bytes: %w", err)
	}
	return RecoveredNode[T]{
		Node:   node,
		Bytes:  bytes,
		Value:  recovered,
		Lambda: lambda,
	}, nil
}

// CanRecoverSingleNode reports whether e_target is in the row span of rows.A.
// targetIndex is the index of the MPT node in the committed source vector B.
func CanRecoverSingleNode(rows []LinearEncodedRow, nodeCount int, targetIndex int) (bool, error) {
	_, err := SolveSingleNodeRecoveryWeights(rows, nodeCount, targetIndex)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrSingleNodeNotRecoverable) {
		return false, nil
	}
	return false, err
}

// SolveSingleNodeRecoveryWeights returns lambda such that lambda^T A = e_target.
// If row.A includes proof padding beyond nodeCount, only the first nodeCount
// dimensions are used.
func SolveSingleNodeRecoveryWeights(rows []LinearEncodedRow, nodeCount int, targetIndex int) ([]fr.Element, error) {
	if err := validateLinearRecoveryInput(rows, nodeCount, targetIndex); err != nil {
		return nil, err
	}

	matrix := make([][]fr.Element, nodeCount)
	for i := 0; i < nodeCount; i++ {
		matrix[i] = make([]fr.Element, len(rows))
		for j := range rows {
			matrix[i][j] = rows[j].A[i]
		}
	}

	rhs := make([]fr.Element, nodeCount)
	rhs[targetIndex].SetOne()

	lambda, ok, err := solveLinearSystemFr(matrix, rhs)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: targetIndex=%d nodeCount=%d rows=%d", ErrSingleNodeNotRecoverable, targetIndex, nodeCount, len(rows))
	}
	return lambda, nil
}

// RecoverSingleNode reconstructs b_target = sum_j lambda_j * rows[j].C.
func RecoverSingleNode(rows []LinearEncodedRow, nodeCount int, targetIndex int) (fr.Element, []fr.Element, error) {
	lambda, err := SolveSingleNodeRecoveryWeights(rows, nodeCount, targetIndex)
	if err != nil {
		return fr.Element{}, nil, err
	}

	var recovered fr.Element
	for i := range rows {
		var term fr.Element
		term.Mul(&lambda[i], &rows[i].C)
		recovered.Add(&recovered, &term)
	}
	return recovered, lambda, nil
}

func validateLinearRecoveryInput(rows []LinearEncodedRow, nodeCount int, targetIndex int) error {
	if len(rows) == 0 {
		return errors.New("empty encoded rows")
	}
	if nodeCount <= 0 {
		return fmt.Errorf("invalid nodeCount %d", nodeCount)
	}
	if targetIndex < 0 {
		return fmt.Errorf("negative targetIndex %d", targetIndex)
	}
	if targetIndex >= nodeCount {
		return fmt.Errorf("targetIndex %d out of range for nodeCount %d", targetIndex, nodeCount)
	}
	for i := range rows {
		if len(rows[i].A) < nodeCount {
			return fmt.Errorf("row %d A length %d shorter than nodeCount %d", i, len(rows[i].A), nodeCount)
		}
	}
	return nil
}

func solveLinearSystemFr(matrix [][]fr.Element, rhs []fr.Element) ([]fr.Element, bool, error) {
	solution, ok, _, err := solveLinearSystemFrWithPivots(matrix, rhs)
	return solution, ok, err
}

type frPivot struct {
	Row int
	Col int
}

func solveLinearSystemFrWithPivots(matrix [][]fr.Element, rhs []fr.Element) ([]fr.Element, bool, []frPivot, error) {
	if len(matrix) == 0 {
		return nil, false, nil, errors.New("empty matrix")
	}
	if len(matrix) != len(rhs) {
		return nil, false, nil, fmt.Errorf("matrix row count %d does not match rhs length %d", len(matrix), len(rhs))
	}
	cols := len(matrix[0])
	for i := range matrix {
		if len(matrix[i]) != cols {
			return nil, false, nil, fmt.Errorf("matrix row %d length %d does not match %d", i, len(matrix[i]), cols)
		}
	}

	aug := make([][]fr.Element, len(matrix))
	for i := range matrix {
		aug[i] = make([]fr.Element, cols+1)
		copy(aug[i], matrix[i])
		aug[i][cols] = rhs[i]
	}

	pivots := make([]frPivot, 0)
	pivotRow := 0
	for col := 0; col < cols && pivotRow < len(aug); col++ {
		selected := -1
		for r := pivotRow; r < len(aug); r++ {
			if !aug[r][col].IsZero() {
				selected = r
				break
			}
		}
		if selected == -1 {
			continue
		}
		aug[pivotRow], aug[selected] = aug[selected], aug[pivotRow]

		var inv fr.Element
		inv.Inverse(&aug[pivotRow][col])
		for k := 0; k <= cols; k++ {
			aug[pivotRow][k].Mul(&aug[pivotRow][k], &inv)
		}

		for r := range aug {
			if r == pivotRow || aug[r][col].IsZero() {
				continue
			}
			factor := aug[r][col]
			for k := 0; k <= cols; k++ {
				var scaled fr.Element
				scaled.Mul(&factor, &aug[pivotRow][k])
				aug[r][k].Sub(&aug[r][k], &scaled)
			}
		}

		pivots = append(pivots, frPivot{Row: pivotRow, Col: col})
		pivotRow++
	}

	for r := range aug {
		allZero := true
		for col := 0; col < cols; col++ {
			if !aug[r][col].IsZero() {
				allZero = false
				break
			}
		}
		if allZero && !aug[r][cols].IsZero() {
			return nil, false, pivots, nil
		}
	}

	solution := make([]fr.Element, cols)
	for _, pivot := range pivots {
		solution[pivot.Col] = aug[pivot.Row][cols]
	}
	return solution, true, pivots, nil
}
