package fileipa

import (
	"encoding/hex"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/folding"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

func TestBuildFoldedFileIPA(t *testing.T) {
	files := smallFiles()
	coeffSets := foldedCoeffSets()

	result, err := BuildFoldedFileIPA(files, coeffSets, "folded-file-ipa-test")
	if err != nil {
		t.Fatalf("build folded file IPA: %v", err)
	}
	if len(result.Rows) != len(coeffSets) {
		t.Fatalf("row count %d != coeff set count %d", len(result.Rows), len(coeffSets))
	}
	sharedQ := sharedQForFiles(t, result.Params, coeffSets[0], files)
	statements, err := rowsToStatements(result.Rows, sharedQ)
	if err != nil {
		t.Fatalf("rows to statements: %v", err)
	}

	root, foldProof, err := folding.FoldStatements(statements, "folded-file-ipa-test")
	if err != nil {
		t.Fatalf("fold rows: %v", err)
	}
	if !challengeLayersEqual(result.FoldProof.Challenges, foldProof.Challenges) {
		t.Fatal("expected folding proof challenges to match")
	}
	ipaOK, err := ipa.Verify(result.Params, root.A, root.Q, root.C, result.IPAProof)
	if err != nil {
		t.Fatalf("verify root IPA: %v", err)
	}
	if !ipaOK {
		t.Fatal("expected root IPA proof to verify")
	}

	ok, err := VerifyFoldedFileIPAResult(result, sharedQ, "folded-file-ipa-test")
	if err != nil {
		t.Fatalf("verify folded file IPA result: %v", err)
	}
	if !ok {
		t.Fatal("expected folded file IPA result to verify")
	}
}

func TestFoldedFileIPATamper(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*FoldedFileIPAResult, *bn254.G1Affine)
	}{
		{
			name: "row C",
			tamper: func(result *FoldedFileIPAResult, sharedQ *bn254.G1Affine) {
				result.Rows[2].C.Add(&result.Rows[2].C, &one)
			},
		},
		{
			name: "fold proof",
			tamper: func(result *FoldedFileIPAResult, sharedQ *bn254.G1Affine) {
				result.FoldProof.Challenges[0][0].Add(&result.FoldProof.Challenges[0][0], &one)
			},
		},
		{
			name: "row A",
			tamper: func(result *FoldedFileIPAResult, sharedQ *bn254.G1Affine) {
				result.Rows[1].A[0].Add(&result.Rows[1].A[0], &one)
			},
		},
		{
			name: "IPA proof",
			tamper: func(result *FoldedFileIPAResult, sharedQ *bn254.G1Affine) {
				result.IPAProof.BFinal.Add(&result.IPAProof.BFinal, &one)
			},
		},
		{
			name: "shared Q",
			tamper: func(result *FoldedFileIPAResult, sharedQ *bn254.G1Affine) {
				*sharedQ = result.IPAProof.L[0]
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := smallFiles()
			result, err := BuildFoldedFileIPA(files, foldedCoeffSets(), "folded-file-ipa-tamper-test")
			if err != nil {
				t.Fatalf("build folded file IPA: %v", err)
			}
			sharedQ := sharedQForFiles(t, result.Params, foldedCoeffSets()[0], files)
			tt.tamper(result, &sharedQ)

			ok, err := VerifyFoldedFileIPAResult(result, sharedQ, "folded-file-ipa-tamper-test")
			if err != nil {
				t.Fatalf("verify tampered folded file IPA: %v", err)
			}
			if ok {
				t.Fatal("expected tampered folded file IPA to be rejected")
			}
		})
	}
}

func TestFoldedFileIPARejectsSingleRow(t *testing.T) {
	files := smallFiles()
	coeffSets := [][]fr.Element{{fe(2), fe(5), fe(7)}}

	if _, err := BuildFoldedFileIPA(files, coeffSets, "folded-file-ipa-single-row-test"); err == nil {
		t.Fatal("expected single-row folded FileIPA to be rejected")
	}
}

func TestFoldedFileIPAVerbose(t *testing.T) {
	domain := "folded-file-ipa-verbose-test"
	files := smallFiles()
	coeffSets := foldedCoeffSets()

	t.Logf("========== Stage 1: Raw input files ==========")
	t.Logf("source file count = %d", len(files))
	t.Logf("domain = %q", domain)
	for i, file := range files {
		t.Logf("file[%d].len = %d bytes", i, len(file))
		t.Logf("file[%d].hex = 0x%s", i, hex.EncodeToString(file))
		t.Logf("file[%d].decimal = %v", i, file)
	}

	t.Logf("========== Stage 2: bytes -> fr chunks ==========")
	chunks, chunkCount, err := filesToFrChunks(coeffSets[0], files)
	if err != nil {
		t.Fatalf("files to fr chunks: %v", err)
	}
	t.Logf("chunk size = %d bytes", frChunkSize)
	t.Logf("31 bytes = 248 bits < bn254/fr modulus size, so each source chunk maps losslessly into one fr.Element")
	for i := range chunks {
		t.Logf("file[%d] chunk count = %d", i, len(chunks[i]))
		for j, chunk := range chunks[i] {
			start := j * frChunkSize
			end := start + frChunkSize
			if end > len(files[i]) {
				end = len(files[i])
			}
			padding := frChunkSize - (end - start)
			t.Logf("file[%d].chunk[%d].raw.hex = 0x%s", i, j, hex.EncodeToString(files[i][start:end]))
			t.Logf("file[%d].chunk[%d].fr = %s", i, j, formatFr(chunk))
			if padding > 0 {
				t.Logf("file[%d].chunk[%d] padding = %d trailing zero byte(s)", i, j, padding)
			}
		}
	}

	t.Logf("========== Stage 3: shared B_flat construction ==========")
	bFlat := flattenFrChunks(chunks)
	bProof := padScalars(bFlat, nextPowerOfTwo(len(bFlat)))
	t.Logf("B_flat = file0_chunks || file1_chunks || ... || file%d_chunks", len(files)-1)
	t.Logf("semantic flat witness length = %d", len(bFlat))
	t.Logf("IPA witness length = %d", len(bProof))
	for i := range files {
		for j := range chunks[i] {
			idx := i*chunkCount + j
			t.Logf("B_flat[%d] = file%d.chunk%d = %s", idx, i, j, formatFr(bFlat[idx]))
		}
	}

	t.Logf("========== Stage 4: per-coeff-set statements ==========")
	params, err := ipa.NewTestParams(len(bProof))
	if err != nil {
		t.Fatalf("ipa params: %v", err)
	}
	Q, err := ipa.CommitB(params, bProof)
	if err != nil {
		t.Fatalf("commit shared witness: %v", err)
	}
	t.Logf("shared Q = CommitB(B_flat padded) = %s", formatG1(Q))
	statements := make([]folding.Statement, len(coeffSets))
	for j, coeffs := range coeffSets {
		aFlat := buildFlatFileCoeffVector(coeffs, chunkCount)
		aProof := padScalars(aFlat, len(bProof))
		c, err := ipa.InnerProduct(aProof, bProof)
		if err != nil {
			t.Fatalf("statement %d inner product: %v", j, err)
		}
		statements[j] = folding.Statement{A: aProof, Q: Q, C: c}
		t.Logf("coeffSets[%d] = %s", j, formatFrVector(coeffs))
		for i := range coeffs {
			for chunkIdx := 0; chunkIdx < chunkCount; chunkIdx++ {
				idx := i*chunkCount + chunkIdx
				t.Logf("A_%d[%d] = coeffSets[%d][%d] = %s", j, idx, j, i, formatFr(aFlat[idx]))
			}
		}
		t.Logf("statement[%d].C = <A_%d, B_flat> = %s", j, j, formatFr(c))
		t.Logf("statement[%d].Q = %s", j, formatG1(statements[j].Q))
	}

	t.Logf("========== Stage 5: folding ==========")
	root, foldProof, err := folding.FoldStatements(statements, domain)
	if err != nil {
		t.Fatalf("fold statements: %v", err)
	}
	foldOK, err := folding.VerifyFold(statements, root, foldProof, domain)
	if err != nil {
		t.Fatalf("verify fold: %v", err)
	}
	if !foldOK {
		t.Fatal("expected folding proof to verify")
	}
	for level, challenges := range foldProof.Challenges {
		for idx, chi := range challenges {
			t.Logf("folding challenge[level=%d][index=%d] = %s", level, idx, formatFr(chi))
		}
	}
	t.Logf("root.A = %s", formatFrVector(root.A))
	t.Logf("root.Q = %s", formatG1(root.Q))
	t.Logf("root.C = %s", formatFr(root.C))

	t.Logf("========== Stage 6: root IPA proof ==========")
	rootProof, qFromProve, cFromProve, err := ipa.Prove(params, root.A, bProof)
	if err != nil {
		t.Fatalf("prove folded root: %v", err)
	}
	ipaOK, err := ipa.Verify(params, root.A, root.Q, root.C, rootProof)
	if err != nil {
		t.Fatalf("verify folded root IPA: %v", err)
	}
	if !ipaOK {
		t.Fatal("expected root IPA proof to verify")
	}
	for i := range rootProof.L {
		t.Logf("proof.L[%d] = %s", i, formatG1(rootProof.L[i]))
		t.Logf("proof.R[%d] = %s", i, formatG1(rootProof.R[i]))
	}
	t.Logf("proof.AFinal = %s", formatFr(rootProof.AFinal))
	t.Logf("proof.BFinal = %s", formatFr(rootProof.BFinal))
	t.Logf("QFromProve = %s", formatG1(qFromProve))
	t.Logf("cFromProve = %s", formatFr(cFromProve))
	t.Logf("root IPA Verify = %v", ipaOK)

	t.Logf("========== Stage 7: end-to-end BuildFoldedFileIPA ==========")
	result, err := BuildFoldedFileIPA(files, coeffSets, domain)
	if err != nil {
		t.Fatalf("build folded file IPA: %v", err)
	}
	sharedQ := sharedQForFiles(t, result.Params, coeffSets[0], files)
	verifyOK, err := VerifyFoldedFileIPAResult(result, sharedQ, domain)
	if err != nil {
		t.Fatalf("verify folded file IPA result: %v", err)
	}
	t.Logf("BuildFoldedFileIPA result.row count = %d", len(result.Rows))
	t.Logf("BuildFoldedFileIPA sharedQ = %s", formatG1(sharedQ))
	t.Logf("VerifyFoldedFileIPAResult = %v", verifyOK)
	if !verifyOK {
		t.Fatal("expected end-to-end folded file IPA result to verify")
	}
}

func foldedCoeffSets() [][]fr.Element {
	return [][]fr.Element{
		{fe(2), fe(5), fe(7)},
		{fe(3), fe(4), fe(11)},
		{fe(6), fe(1), fe(9)},
		{fe(8), fe(10), fe(12)},
	}
}
