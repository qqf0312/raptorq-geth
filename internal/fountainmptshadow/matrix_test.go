package fountainmptshadow

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func TestGenerateMatrixDeterministicAndSystematic(t *testing.T) {
	first, err := GenerateMatrix([]byte("seed"), 3, 5)
	if err != nil {
		t.Fatalf("GenerateMatrix: %v", err)
	}
	second, err := GenerateMatrix([]byte("seed"), 3, 5)
	if err != nil {
		t.Fatalf("GenerateMatrix second: %v", err)
	}
	if len(first) != 8 {
		t.Fatalf("row count %d, want 8", len(first))
	}
	if !matrixEqual(first, second) {
		t.Fatal("same seed generated different matrices")
	}
	var one fr.Element
	one.SetOne()
	for row := 0; row < 3; row++ {
		for col := 0; col < 3; col++ {
			if row == col {
				if !first[row][col].Equal(&one) {
					t.Fatalf("identity entry [%d][%d] is not one", row, col)
				}
			} else if !first[row][col].IsZero() {
				t.Fatalf("identity entry [%d][%d] is non-zero", row, col)
			}
		}
	}
}

func matrixEqual(a, b [][]fr.Element) bool {
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
