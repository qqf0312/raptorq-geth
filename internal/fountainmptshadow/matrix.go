package fountainmptshadow

import (
	"encoding/binary"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/crypto"
)

const matrixDomain = "fountainmptshadow/matrix/v1"

// GenerateMatrix deterministically builds a systematic Fr matrix. The explicit
// minimumRows == 1 mode returns one seed-derived repair row for distributed
// storage; it is not sufficient to recover a multi-source path by itself.
// Other modes retain the full-rank identity prefix and power-of-two row count.
func GenerateMatrix(seed []byte, columns, minimumRows int) ([][]fr.Element, error) {
	if len(seed) == 0 {
		return nil, fmt.Errorf("empty matrix seed")
	}
	if columns <= 0 {
		return nil, fmt.Errorf("invalid matrix column count %d", columns)
	}
	if minimumRows == 1 {
		matrix := make([][]fr.Element, 1)
		matrix[0] = make([]fr.Element, columns)
		fillRepairRow(matrix[0], seed, 0)
		return matrix, nil
	}
	rowCount := columns
	if minimumRows > rowCount {
		rowCount = minimumRows
	}
	if rowCount < 2 {
		rowCount = 2
	}
	rowCount = nextPowerOfTwo(rowCount)

	matrix := make([][]fr.Element, rowCount)
	for row := range matrix {
		matrix[row] = make([]fr.Element, columns)
		if row < columns {
			matrix[row][row].SetOne()
			continue
		}
		fillRepairRow(matrix[row], seed, row)
	}
	return matrix, nil
}

func fillRepairRow(row []fr.Element, seed []byte, rowIndex int) {
	selected := 0
	for col := range row {
		digest := matrixDigest(seed, rowIndex, col)
		// A one-in-four inclusion rate keeps repair rows sparse.
		if digest[0]&3 != 0 {
			continue
		}
		row[col].SetBytes(digest)
		if row[col].IsZero() {
			row[col].SetOne()
		}
		selected++
	}
	target := minInt(2, len(row))
	for offset := 0; selected < target && offset < len(row); offset++ {
		col := (rowIndex + offset) % len(row)
		if row[col].IsZero() {
			row[col].SetOne()
			selected++
		}
	}
}

func matrixDigest(seed []byte, row, col int) []byte {
	var indexes [16]byte
	binary.BigEndian.PutUint64(indexes[:8], uint64(row))
	binary.BigEndian.PutUint64(indexes[8:], uint64(col))
	return crypto.Keccak256([]byte(matrixDomain), seed, indexes[:])
}

func nextPowerOfTwo(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
