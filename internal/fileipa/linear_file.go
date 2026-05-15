package fileipa

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/folding"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

const frChunkSize = 31

// FileIPAResult is the proof bundle for one fr-native flat file inner-product
// IPA statement:
//
//	c = <A_flat, B_flat>
//
// B_flat is the file-priority concatenation of all 31-byte source chunks, and
// A_flat repeats coeffs[i] for every chunk of file i. A_flat is stored after
// zero padding to the IPA vector length.
type FileIPAResult struct {
	Params *ipa.Params
	Row    FileIPARow
	Proof  *ipa.Proof
}

// FileIPARow is the public row sent by the prover for file IPA verification.
// The shared witness commitment Q is not carried per row; the verifier supplies
// its already-known shared Q when verifying.
type FileIPARow struct {
	A []fr.Element
	C fr.Element
}

// BytesToFrChunks converts data into bn254/fr chunks using fixed big-endian
// packing. Each chunk carries up to 31 bytes in bytes 1..31 of a 32-byte
// big-endian buffer, with byte 0 fixed to zero and the final chunk padded with
// trailing zeros. Every chunk integer is at most 248 bits and is strictly below
// the fr modulus. Empty input returns no chunks.
func BytesToFrChunks(data []byte) ([]fr.Element, error) {
	if len(data) == 0 {
		return nil, nil
	}
	chunks := make([]fr.Element, (len(data)+frChunkSize-1)/frChunkSize)
	for i := range chunks {
		start := i * frChunkSize
		end := start + frChunkSize
		if end > len(data) {
			end = len(data)
		}
		var buf [32]byte
		copy(buf[1:], data[start:end])
		chunks[i].SetBytes(buf[:])
	}
	return chunks, nil
}

// FrChunksToBytes reverses BytesToFrChunks for source chunks. It serializes
// each fr.Element to 32-byte big-endian form, requires the top byte to be zero,
// appends the lower 31 bytes, and truncates to originalLen to remove padding.
func FrChunksToBytes(chunks []fr.Element, originalLen int) ([]byte, error) {
	if originalLen < 0 {
		return nil, fmt.Errorf("negative original length")
	}
	if originalLen > len(chunks)*frChunkSize {
		return nil, fmt.Errorf("original length %d exceeds chunk capacity %d", originalLen, len(chunks)*frChunkSize)
	}
	out := make([]byte, 0, len(chunks)*frChunkSize)
	for i := range chunks {
		buf := chunks[i].Bytes()
		if buf[0] != 0 {
			return nil, fmt.Errorf("chunk %d exceeds 31-byte representation", i)
		}
		out = append(out, buf[1:]...)
	}
	return out[:originalLen], nil
}

// BuildFileIPA builds one fr-native flat linear file IPA proof. It does not
// use folding; folding is only used by BuildFoldedFileIPA for multiple
// coefficient sets over one shared file witness.
func BuildFileIPA(coeffs []fr.Element, files [][]byte, domain string) (*FileIPAResult, error) {
	_ = domain
	statement, params, proof, _, _, _, _, err := buildFileIPAStatement(coeffs, files)
	if err != nil {
		return nil, err
	}
	return &FileIPAResult{
		Params: params,
		Row: FileIPARow{
			A: cloneScalars(statement.A),
			C: statement.C,
		},
		Proof: proof,
	}, nil
}

// VerifyFileIPAResult verifies one flat file IPA row. The prover sends only
// row = (A, C) plus IPAProof; it does not send B, Q, or P. The verifier uses
// its already-known shared Q = CommitB(B) and the IPA verifier checks that
// C = <A, B> for the B bound by sharedQ, without learning B.
func VerifyFileIPAResult(result *FileIPAResult, sharedQ bn254.G1Affine, domain string) (bool, error) {
	_ = domain
	if result == nil {
		return false, fmt.Errorf("nil file IPA result")
	}
	if len(result.Row.A) == 0 {
		return false, nil
	}
	return ipa.Verify(result.Params, result.Row.A, sharedQ, result.Row.C, result.Proof)
}

func buildFileIPAStatement(coeffs []fr.Element, files [][]byte) (folding.Statement, *ipa.Params, *ipa.Proof, int, int, []int, int, error) {
	chunks, chunkCount, err := filesToFrChunks(coeffs, files)
	if err != nil {
		return folding.Statement{}, nil, nil, 0, 0, nil, 0, err
	}
	aFlat := buildFlatFileCoeffVector(coeffs, chunkCount)
	bFlat := flattenFrChunks(chunks)
	if len(aFlat) != len(bFlat) {
		return folding.Statement{}, nil, nil, 0, 0, nil, 0, fmt.Errorf("flat vector length mismatch: %d != %d", len(aFlat), len(bFlat))
	}
	aProof, bProof := padProofVectors(aFlat, bFlat)
	params, err := ipa.NewTestParams(len(bProof))
	if err != nil {
		return folding.Statement{}, nil, nil, 0, 0, nil, 0, fmt.Errorf("create IPA params: %w", err)
	}
	q, c, proof, err := proveStatement(params, aProof, bProof)
	if err != nil {
		return folding.Statement{}, nil, nil, 0, 0, nil, 0, err
	}
	return folding.Statement{A: aProof, Q: q, C: c}, params, proof, len(aFlat), len(aProof), fileLengths(files), chunkCount, nil
}

func proveStatement(params *ipa.Params, aProof, bProof []fr.Element) (bn254.G1Affine, fr.Element, *ipa.Proof, error) {
	c, err := ipa.InnerProduct(aProof, bProof)
	if err != nil {
		return bn254.G1Affine{}, fr.Element{}, nil, fmt.Errorf("flat inner product: %w", err)
	}
	q, err := ipa.CommitB(params, bProof)
	if err != nil {
		return bn254.G1Affine{}, fr.Element{}, nil, fmt.Errorf("commit flat witness: %w", err)
	}
	proof, qFromProve, cFromProve, err := ipa.Prove(params, aProof, bProof)
	if err != nil {
		return bn254.G1Affine{}, fr.Element{}, nil, fmt.Errorf("prove flat file relation: %w", err)
	}
	if !qFromProve.Equal(&q) {
		return bn254.G1Affine{}, fr.Element{}, nil, fmt.Errorf("IPA prover commitment does not match flat witness commitment")
	}
	if !cFromProve.Equal(&c) {
		return bn254.G1Affine{}, fr.Element{}, nil, fmt.Errorf("IPA prover inner product does not match flat file relation")
	}
	return q, c, proof, nil
}

func filesToFrChunks(coeffs []fr.Element, files [][]byte) ([][]fr.Element, int, error) {
	if len(files) == 0 {
		return nil, 0, fmt.Errorf("empty files")
	}
	if len(coeffs) != len(files) {
		return nil, 0, fmt.Errorf("coeff/file count mismatch: %d != %d", len(coeffs), len(files))
	}
	fileLen := len(files[0])
	if fileLen == 0 {
		return nil, 0, fmt.Errorf("empty file")
	}
	for i := range files {
		if len(files[i]) != fileLen {
			return nil, 0, fmt.Errorf("file %d length %d != %d", i, len(files[i]), fileLen)
		}
	}

	chunks := make([][]fr.Element, len(files))
	chunkCount := -1
	for i := range files {
		fileChunks, err := BytesToFrChunks(files[i])
		if err != nil {
			return nil, 0, err
		}
		if chunkCount == -1 {
			chunkCount = len(fileChunks)
		} else if len(fileChunks) != chunkCount {
			return nil, 0, fmt.Errorf("file %d chunk count %d != %d", i, len(fileChunks), chunkCount)
		}
		chunks[i] = fileChunks
	}
	if chunkCount <= 0 {
		return nil, 0, fmt.Errorf("empty chunk set")
	}
	return chunks, chunkCount, nil
}

func buildFlatFileCoeffVector(coeffs []fr.Element, chunkCount int) []fr.Element {
	if chunkCount <= 0 {
		return nil
	}
	out := make([]fr.Element, len(coeffs)*chunkCount)
	for i := range coeffs {
		for t := 0; t < chunkCount; t++ {
			out[i*chunkCount+t] = coeffs[i]
		}
	}
	return out
}

func flattenFrChunks(chunks [][]fr.Element) []fr.Element {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]fr.Element, 0, len(chunks)*len(chunks[0]))
	for i := range chunks {
		out = append(out, chunks[i]...)
	}
	return out
}

func padProofVectors(aFlat, bFlat []fr.Element) ([]fr.Element, []fr.Element) {
	n := nextPowerOfTwo(len(aFlat))
	return padScalars(aFlat, n), padScalars(bFlat, n)
}

func fileLengths(files [][]byte) []int {
	out := make([]int, len(files))
	for i := range files {
		out[i] = len(files[i])
	}
	return out
}

func padScalars(in []fr.Element, n int) []fr.Element {
	if len(in) >= n {
		return append([]fr.Element(nil), in...)
	}
	out := make([]fr.Element, n)
	copy(out, in)
	return out
}

func nextPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

func cloneScalars(in []fr.Element) []fr.Element {
	return append([]fr.Element(nil), in...)
}

func cloneScalarMatrix(in [][]fr.Element) [][]fr.Element {
	out := make([][]fr.Element, len(in))
	for i := range in {
		out[i] = cloneScalars(in[i])
	}
	return out
}

func frSlicesEqual(a, b []fr.Element) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(&b[i]) {
			return false
		}
	}
	return true
}

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
