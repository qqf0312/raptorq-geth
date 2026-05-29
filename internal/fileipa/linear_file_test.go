package fileipa

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

func TestBytesToFrChunksRoundTrip(t *testing.T) {
	lengths := []int{0, 1, 30, 31, 32, 62, 63, 70}
	for _, n := range lengths {
		t.Run("", func(t *testing.T) {
			data := deterministicBytes(n)
			chunks, err := BytesToFrChunks(data)
			if err != nil {
				t.Fatalf("bytes to fr chunks: %v", err)
			}
			roundTrip, err := FrChunksToBytes(chunks, len(data))
			if err != nil {
				t.Fatalf("fr chunks to bytes: %v", err)
			}
			if !bytes.Equal(roundTrip, data) {
				t.Fatalf("round trip mismatch for length %d", n)
			}
		})
	}
}

func TestFrChunksToBytesRejectsOversizedChunk(t *testing.T) {
	var chunk fr.Element
	chunk.SetBytes(bytes.Repeat([]byte{0xff}, 32))

	if _, err := FrChunksToBytes([]fr.Element{chunk}, 31); err == nil {
		t.Fatal("expected oversized source chunk to be rejected")
	}
}

func TestBuildFlatFileCoeffVector(t *testing.T) {
	coeffs := []fr.Element{fe(2), fe(5), fe(7)}
	got := buildFlatFileCoeffVector(coeffs, 3)
	want := []fr.Element{fe(2), fe(2), fe(2), fe(5), fe(5), fe(5), fe(7), fe(7), fe(7)}

	if !frSlicesEqual(got, want) {
		t.Fatalf("flat coeff vector mismatch: got %s want %s", formatFrVector(got), formatFrVector(want))
	}
}

func TestFlatFileInnerProduct(t *testing.T) {
	coeffs := []fr.Element{fe(2), fe(5), fe(7)}
	files := smallFiles()
	chunks, chunkCount, err := filesToFrChunks(coeffs, files)
	if err != nil {
		t.Fatalf("files to fr chunks: %v", err)
	}
	aFlat := buildFlatFileCoeffVector(coeffs, chunkCount)
	bFlat := flattenFrChunks(chunks)
	aProof, bProof := padProofVectors(aFlat, bFlat)

	var manual fr.Element
	for i := range files {
		for t := range chunks[i] {
			var term fr.Element
			term.Mul(&coeffs[i], &chunks[i][t])
			manual.Add(&manual, &term)
		}
	}
	got, err := ipa.InnerProduct(aProof, bProof)
	if err != nil {
		t.Fatalf("flat inner product: %v", err)
	}
	if !got.Equal(&manual) {
		t.Fatalf("flat inner product mismatch: got %s want %s", formatFr(got), formatFr(manual))
	}
}

func TestBuildFileIPA(t *testing.T) {
	coeffs := []fr.Element{fe(2), fe(5), fe(7)}
	files := smallFiles()

	t.Logf("========== TestBuildFileIPA: raw inputs ==========")
	t.Logf("coeffs = %s", formatFrVector(coeffs))
	for i, file := range files {
		t.Logf("file[%d].len = %d bytes", i, len(file))
		t.Logf("file[%d].hex = 0x%s", i, hex.EncodeToString(file))
		t.Logf("file[%d].decimal = %v", i, file)
	}

	result, err := BuildFileIPA(coeffs, files, "file-ipa-test")
	if err != nil {
		t.Fatalf("build file IPA: %v", err)
	}
	chunks, chunkCount, err := filesToFrChunks(coeffs, files)
	if err != nil {
		t.Fatalf("files to fr chunks: %v", err)
	}
	aFlat := buildFlatFileCoeffVector(coeffs, chunkCount)
	bFlat := flattenFrChunks(chunks)
	aProof, bProof := padProofVectors(aFlat, bFlat)

	t.Logf("========== TestBuildFileIPA: bytes -> fr chunks ==========")
	t.Logf("chunk size = %d bytes", frChunkSize)
	t.Logf("chunkCount = %d", chunkCount)
	for i := range chunks {
		t.Logf("file[%d] chunks = %d", i, len(chunks[i]))
		for chunkIdx, chunk := range chunks[i] {
			start := chunkIdx * frChunkSize
			end := start + frChunkSize
			if end > len(files[i]) {
				end = len(files[i])
			}
			padding := frChunkSize - (end - start)
			t.Logf("file[%d].chunk[%d].raw.hex = 0x%s", i, chunkIdx, hex.EncodeToString(files[i][start:end]))
			t.Logf("file[%d].chunk[%d].fr = %s", i, chunkIdx, formatFr(chunk))
			if padding > 0 {
				t.Logf("file[%d].chunk[%d].padding = %d trailing zero byte(s)", i, chunkIdx, padding)
			}
		}
	}

	t.Logf("========== TestBuildFileIPA: flat vectors ==========")
	t.Logf("A_flat = %s", formatFrVector(aFlat))
	t.Logf("B_flat = %s", formatFrVector(bFlat))
	t.Logf("A_flat_padded = %s", formatFrVector(aProof))
	t.Logf("B_flat_padded = %s", formatFrVector(bProof))
	t.Logf("SemanticLen = %d, IPALen = %d", len(aFlat), len(aProof))

	var manualC fr.Element
	t.Logf("========== TestBuildFileIPA: manual inner product ==========")
	for i := range files {
		for chunkIdx := range chunks[i] {
			flatIdx := i*chunkCount + chunkIdx
			var term fr.Element
			term.Mul(&coeffs[i], &chunks[i][chunkIdx])
			manualC.Add(&manualC, &term)
			t.Logf(
				"term file%d.chunk%d: A_flat[%d](%s) * B_flat[%d](%s) = %s",
				i, chunkIdx, flatIdx, formatFr(aFlat[flatIdx]), flatIdx, formatFr(bFlat[flatIdx]), formatFr(term),
			)
		}
	}
	t.Logf("manual C = %s", formatFr(manualC))
	t.Logf("result.Row.C = %s", formatFr(result.Row.C))
	if !result.Row.C.Equal(&manualC) {
		t.Fatalf("C mismatch: got %s want %s", formatFr(result.Row.C), formatFr(manualC))
	}
	if !frSlicesEqual(result.Row.A, aProof) {
		t.Fatal("row A does not match padded flat coefficient vector")
	}

	qExpected, err := ipa.CommitB(result.Params, bProof)
	if err != nil {
		t.Fatalf("commit B_flat padded: %v", err)
	}
	t.Logf("========== TestBuildFileIPA: commitment ==========")
	t.Logf("QExpected = CommitB(B_flat_padded) = %s", formatG1(qExpected))

	ipaOK, err := ipa.Verify(result.Params, aProof, qExpected, result.Row.C, result.Proof)
	if err != nil {
		t.Fatalf("ipa verify: %v", err)
	}
	t.Logf("========== TestBuildFileIPA: IPA proof ==========")
	for i := range result.Proof.L {
		t.Logf("proof.L[%d] = %s", i, formatG1(result.Proof.L[i]))
		t.Logf("proof.R[%d] = %s", i, formatG1(result.Proof.R[i]))
	}
	t.Logf("proof.AFinal = %s", formatFr(result.Proof.AFinal))
	t.Logf("proof.BFinal = %s", formatFr(result.Proof.BFinal))
	t.Logf("ipa.Verify = %v", ipaOK)
	if !ipaOK {
		t.Fatal("expected direct IPA verification to pass")
	}

	ok, err := VerifyFileIPAResult(result, qExpected, "file-ipa-test")
	if err != nil {
		t.Fatalf("verify file IPA: %v", err)
	}
	if !ok {
		t.Fatal("expected file IPA to verify")
	}
	t.Logf("VerifyFileIPAResult = %v", ok)
}

func TestFileIPATamper(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*FileIPAResult, *bn254.G1Affine)
	}{
		{
			name: "A",
			tamper: func(result *FileIPAResult, sharedQ *bn254.G1Affine) {
				result.Row.A[1].Add(&result.Row.A[1], &one)
			},
		},
		{
			name: "C",
			tamper: func(result *FileIPAResult, sharedQ *bn254.G1Affine) {
				result.Row.C.Add(&result.Row.C, &one)
			},
		},
		{
			name: "shared Q",
			tamper: func(result *FileIPAResult, sharedQ *bn254.G1Affine) {
				*sharedQ = result.Proof.L[0]
			},
		},
		{
			name: "IPA proof",
			tamper: func(result *FileIPAResult, sharedQ *bn254.G1Affine) {
				result.Proof.BFinal.Add(&result.Proof.BFinal, &one)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coeffs := []fr.Element{fe(2), fe(5), fe(7)}
			files := smallFiles()
			result, err := BuildFileIPA(coeffs, files, "file-ipa-tamper-test")
			if err != nil {
				t.Fatalf("build file IPA: %v", err)
			}
			sharedQ := sharedQForFiles(t, result.Params, coeffs, files)
			tt.tamper(result, &sharedQ)

			ok, err := VerifyFileIPAResult(result, sharedQ, "file-ipa-tamper-test")
			if err != nil {
				t.Fatalf("verify tampered file IPA: %v", err)
			}
			if ok {
				t.Fatal("expected tampered file IPA to be rejected")
			}
		})
	}
}

func sharedQForFiles(t *testing.T, params *ipa.Params, coeffs []fr.Element, files [][]byte) bn254.G1Affine {
	t.Helper()
	chunks, _, err := filesToFrChunks(coeffs, files)
	if err != nil {
		t.Fatalf("files to fr chunks: %v", err)
	}
	bFlat := flattenFrChunks(chunks)
	bProof := padScalars(bFlat, nextPowerOfTwo(len(bFlat)))
	q, err := ipa.CommitB(params, bProof)
	if err != nil {
		t.Fatalf("commit shared witness: %v", err)
	}
	return q
}

func TestFileIPAInvalidInput(t *testing.T) {
	coeffs := []fr.Element{fe(1), fe(2)}

	tests := []struct {
		name   string
		coeffs []fr.Element
		files  [][]byte
	}{
		{
			name:   "empty files",
			coeffs: nil,
			files:  nil,
		},
		{
			name:   "empty file",
			coeffs: []fr.Element{fe(1)},
			files:  [][]byte{{}},
		},
		{
			name:   "coeff count mismatch",
			coeffs: coeffs[:1],
			files:  [][]byte{deterministicBytes(31), deterministicBytesWithOffset(31, 1)},
		},
		{
			name:   "file length mismatch",
			coeffs: coeffs,
			files:  [][]byte{deterministicBytes(31), deterministicBytesWithOffset(32, 1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BuildFileIPA(tt.coeffs, tt.files, "file-ipa-invalid-test"); err == nil {
				t.Fatal("expected invalid input to be rejected")
			}
		})
	}
}

func smallFiles() [][]byte {
	return [][]byte{
		deterministicBytesWithOffset(65, 0),
		deterministicBytesWithOffset(65, 17),
		deterministicBytesWithOffset(65, 41),
	}
}

func deterministicBytes(n int) []byte {
	return deterministicBytesWithOffset(n, 0)
}

func deterministicBytesWithOffset(n, offset int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte((i*31 + offset + 1) % 251)
	}
	return out
}

func fe(v uint64) fr.Element {
	var out fr.Element
	out.SetUint64(v)
	return out
}

var one = fe(1)

func formatFr(e fr.Element) string {
	var n big.Int
	e.BigInt(&n)
	return n.String()
}

func formatFrVector(v []fr.Element) string {
	parts := make([]string, len(v))
	for i := range v {
		parts[i] = formatFr(v[i])
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatG1(p bn254.G1Affine) string {
	raw := p.RawBytes()
	return "0x" + hex.EncodeToString(raw[:])
}
