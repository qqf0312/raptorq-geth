package fountainmptshadow

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/folding"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg/linearrecovery"
)

// BuildEncodedPath frames one original path, generates its seed-derived
// systematic matrix, computes vector encoded blocks, and folds one scalar IPA
// statement per encoded chunk into a single proof.
func BuildEncodedPath(height uint64, root common.Hash, key []byte, nodes [][]byte, seed []byte, minimumRows int) (*EncodedPath, error) {
	files, layout, err := FramePathNodes(nodes)
	if err != nil {
		return nil, err
	}
	matrix, err := GenerateMatrix(seed, len(files), minimumRows)
	if err != nil {
		return nil, err
	}
	blocks, err := encodeBlocks(matrix, files)
	if err != nil {
		return nil, err
	}
	proof, q, err := buildPathProof(files, matrix, blocks)
	if err != nil {
		return nil, err
	}
	result := &EncodedPath{
		Height: height,
		Root:   root,
		Key:    append([]byte(nil), key...),
		Layout: layout,
		Seed:   append([]byte(nil), seed...),
		Matrix: cloneMatrix(matrix),
		Blocks: blocks,
		Q:      q,
		Proof:  proof,
	}
	if err := validateEncodedPath(result); err != nil {
		return nil, err
	}
	return result, nil
}

func buildPathProof(files [][]byte, matrix, blocks [][]fr.Element) (*fileipa.FoldedFileIPAResult, bn254.G1Affine, error) {
	witness, err := compactWitness(files)
	if err != nil {
		return nil, bn254.G1Affine{}, err
	}
	rows := make([]fileipa.FileIPARow, 0, len(matrix)*len(blocks[0]))
	for row := range matrix {
		for chunk := range blocks[row] {
			a, err := expandCoefficientChunkRow(matrix[row], len(blocks[row]), chunk, len(witness))
			if err != nil {
				return nil, bn254.G1Affine{}, err
			}
			rows = append(rows, fileipa.FileIPARow{A: a, C: blocks[row][chunk]})
		}
	}
	return buildFoldedWitnessProof(witness, rows, latestPathDomain)
}

func buildFoldedWitnessProof(witness []fr.Element, rows []fileipa.FileIPARow, domain string) (*fileipa.FoldedFileIPAResult, bn254.G1Affine, error) {
	params, err := ipa.NewTestParams(len(witness))
	if err != nil {
		return nil, bn254.G1Affine{}, fmt.Errorf("create folded witness IPA params: %w", err)
	}
	return buildFoldedWitnessProofWithParams(params, witness, rows, domain)
}

func buildFoldedWitnessProofWithParams(params *ipa.Params, witness []fr.Element, rows []fileipa.FileIPARow, domain string) (*fileipa.FoldedFileIPAResult, bn254.G1Affine, error) {
	if len(rows) == 0 {
		return nil, bn254.G1Affine{}, fmt.Errorf("empty folded witness rows")
	}
	q, err := ipa.CommitB(params, witness)
	if err != nil {
		return nil, bn254.G1Affine{}, fmt.Errorf("commit folded witness: %w", err)
	}
	statements := make([]folding.Statement, len(rows))
	for i := range rows {
		c, err := ipa.InnerProduct(rows[i].A, witness)
		if err != nil {
			return nil, bn254.G1Affine{}, fmt.Errorf("row %d inner product: %w", i, err)
		}
		if !c.Equal(&rows[i].C) {
			return nil, bn254.G1Affine{}, fmt.Errorf("row %d encoded chunk does not match witness", i)
		}
		statements[i] = folding.Statement{A: append([]fr.Element(nil), rows[i].A...), Q: q, C: c}
	}
	for len(statements) < nextPowerOfTwo(len(statements)) {
		statements = append(statements, folding.Statement{A: make([]fr.Element, len(witness)), Q: q})
	}
	root, foldProof, err := folding.FoldStatements(statements, domain)
	if err != nil {
		return nil, bn254.G1Affine{}, fmt.Errorf("fold witness rows: %w", err)
	}
	proof, qFromProof, cFromProof, err := ipa.Prove(params, root.A, witness)
	if err != nil {
		return nil, bn254.G1Affine{}, fmt.Errorf("prove folded witness rows: %w", err)
	}
	if !qFromProof.Equal(&q) || !cFromProof.Equal(&root.C) {
		return nil, bn254.G1Affine{}, fmt.Errorf("folded witness IPA statement mismatch")
	}
	proofRows := make([]fileipa.FileIPARow, len(statements))
	for i := range statements {
		proofRows[i] = fileipa.FileIPARow{A: append([]fr.Element(nil), statements[i].A...), C: statements[i].C}
	}
	challenges := make([][]fr.Element, len(foldProof.Challenges))
	for i := range foldProof.Challenges {
		challenges[i] = append([]fr.Element(nil), foldProof.Challenges[i]...)
	}
	return &fileipa.FoldedFileIPAResult{
		Params:    params,
		Rows:      proofRows,
		FoldProof: &fileipa.FileFoldProof{Challenges: challenges},
		IPAProof:  proof,
	}, q, nil
}

func VerifyEncodedPath(encoded *EncodedPath) (bool, error) {
	if err := validateEncodedPath(encoded); err != nil {
		return false, err
	}
	if encoded.Aggregate != nil {
		return VerifyEpochAggregate(encoded.Aggregate)
	}
	return fileipa.VerifyFoldedFileIPAResult(encoded.Proof, encoded.Q, latestPathDomain)
}

// RecoverPath reconstructs every framed source node from the generation matrix
// and encoded block chunks.
func RecoverPath(encoded *EncodedPath) ([][]byte, error) {
	if err := validateEncodedPath(encoded); err != nil {
		return nil, err
	}
	chunkCount := len(encoded.Blocks[0])
	files := make([][]byte, len(encoded.Matrix[0]))
	for source := range files {
		chunks := make([]fr.Element, chunkCount)
		for chunk := 0; chunk < chunkCount; chunk++ {
			column := make([]fr.Element, len(encoded.Blocks))
			for row := range encoded.Blocks {
				column[row] = encoded.Blocks[row][chunk]
			}
			value, _, err := linearrecovery.RecoverSingleNodeFromMatrix(encoded.Matrix, column, source)
			if err != nil {
				return nil, fmt.Errorf("recover source %d chunk %d: %w", source, chunk, err)
			}
			chunks[chunk] = value
		}
		file, err := fileipa.FrChunksToBytes(chunks, int(encoded.Layout.FileSize))
		if err != nil {
			return nil, fmt.Errorf("recover source file %d: %w", source, err)
		}
		files[source] = file
	}
	return UnframePathNodes(files, encoded.Layout)
}

// BuildHistoricalLeaf builds one coordinate-selector statement per Fr chunk
// of the trailing leaf record and folds them into one proof.
func BuildHistoricalLeaf(startHeight, terminalHeight uint64, terminalRoot common.Hash, key []byte, nodes [][]byte) (*HistoricalLeaf, error) {
	files, layout, err := FramePathNodes(nodes)
	if err != nil {
		return nil, err
	}
	witness, err := compactWitness(files)
	if err != nil {
		return nil, err
	}
	leafRecord, err := frameCompactLeaf(nodes[len(nodes)-1])
	if err != nil {
		return nil, err
	}
	block, err := fileipa.BytesToFrChunks(leafRecord)
	if err != nil {
		return nil, err
	}
	selector, err := historicalSelector(layout, len(block), len(witness))
	if err != nil {
		return nil, err
	}
	params, err := ipa.NewTestParams(len(witness))
	if err != nil {
		return nil, fmt.Errorf("create historical leaf IPA params: %w", err)
	}
	selectStart := uint64(int(layout.CompactBytes)/SourceChunkSize - len(block))
	rows := make([]fileipa.FileIPARow, len(block))
	for chunk := range block {
		a, err := historicalChunkSelector(selectStart, chunk, len(witness))
		if err != nil {
			return nil, err
		}
		rows[chunk] = fileipa.FileIPARow{A: a, C: block[chunk]}
	}
	proof, q, err := buildFoldedWitnessProofWithParams(params, witness, rows, historicalLeafDomain)
	if err != nil {
		return nil, fmt.Errorf("build historical leaf IPA: %w", err)
	}
	result := &HistoricalLeaf{
		StartHeight:    startHeight,
		TerminalHeight: terminalHeight,
		TerminalRoot:   terminalRoot,
		Key:            append([]byte(nil), key...),
		Layout:         layout,
		Leaf:           append([]byte(nil), nodes[len(nodes)-1]...),
		Selector:       append([]fr.Element(nil), selector...),
		Block:          block,
		Q:              q,
		Proof:          proof,
	}
	if err := validateHistoricalLeaf(result); err != nil {
		return nil, err
	}
	return result, nil
}

func VerifyHistoricalLeaf(leaf *HistoricalLeaf) (bool, error) {
	if err := validateHistoricalLeaf(leaf); err != nil {
		return false, err
	}
	if leaf.Aggregate != nil {
		return VerifyEpochAggregate(leaf.Aggregate)
	}
	return fileipa.VerifyFoldedFileIPAResult(leaf.Proof, leaf.Q, historicalLeafDomain)
}

// DecodeHistoricalLeaf returns the canonical leaf selected by the single row.
func DecodeHistoricalLeaf(leaf *HistoricalLeaf) ([]byte, error) {
	if err := validateHistoricalLeaf(leaf); err != nil {
		return nil, err
	}
	return append([]byte(nil), leaf.Leaf...), nil
}

func encodeBlocks(matrix [][]fr.Element, files [][]byte) ([][]fr.Element, error) {
	if len(matrix) == 0 || len(files) == 0 {
		return nil, fmt.Errorf("empty matrix or files")
	}
	chunks := make([][]fr.Element, len(files))
	chunkCount := -1
	for i := range files {
		values, err := fileipa.BytesToFrChunks(files[i])
		if err != nil {
			return nil, err
		}
		if chunkCount == -1 {
			chunkCount = len(values)
		} else if len(values) != chunkCount {
			return nil, fmt.Errorf("file %d chunk count %d != %d", i, len(values), chunkCount)
		}
		chunks[i] = values
	}
	blocks := make([][]fr.Element, len(matrix))
	for row := range matrix {
		if len(matrix[row]) != len(files) {
			return nil, fmt.Errorf("matrix row %d length %d != file count %d", row, len(matrix[row]), len(files))
		}
		blocks[row] = make([]fr.Element, chunkCount)
		for source := range files {
			for chunk := 0; chunk < chunkCount; chunk++ {
				var term fr.Element
				term.Mul(&matrix[row][source], &chunks[source][chunk])
				blocks[row][chunk].Add(&blocks[row][chunk], &term)
			}
		}
	}
	return blocks, nil
}

func compactWitness(files [][]byte) ([]fr.Element, error) {
	var flat []fr.Element
	for i := range files {
		chunks, err := fileipa.BytesToFrChunks(files[i])
		if err != nil {
			return nil, fmt.Errorf("file %d chunks: %w", i, err)
		}
		flat = append(flat, chunks...)
	}
	padded := make([]fr.Element, nextPowerOfTwo(len(flat)))
	copy(padded, flat)
	return padded, nil
}

func historicalSelector(layout PathLayout, leafChunks, vectorLength int) ([]fr.Element, error) {
	compactChunks := int(layout.CompactBytes) / SourceChunkSize
	if leafChunks <= 0 || compactChunks < leafChunks || vectorLength < compactChunks {
		return nil, fmt.Errorf("invalid historical leaf selector dimensions")
	}
	selector := make([]fr.Element, vectorLength)
	for i := compactChunks - leafChunks; i < compactChunks; i++ {
		selector[i].SetOne()
	}
	return selector, nil
}

func historicalChunkSelector(selectStart uint64, chunk, vectorLength int) ([]fr.Element, error) {
	if chunk < 0 || selectStart > uint64(vectorLength) ||
		uint64(chunk) >= uint64(vectorLength)-selectStart {
		return nil, fmt.Errorf("historical chunk %d is outside vector length %d", chunk, vectorLength)
	}
	selector := make([]fr.Element, vectorLength)
	selector[int(selectStart)+chunk].SetOne()
	return selector, nil
}

func validateEncodedPath(encoded *EncodedPath) error {
	if encoded == nil {
		return fmt.Errorf("nil encoded path")
	}
	if err := validateLayout(encoded.Layout); err != nil {
		return err
	}
	if encoded.Proof == nil && encoded.Aggregate == nil {
		return fmt.Errorf("nil folded path proof")
	}
	if len(encoded.Matrix) == 0 || len(encoded.Matrix) != len(encoded.Blocks) {
		return fmt.Errorf("matrix/block row count mismatch")
	}
	chunkCount := len(encoded.Blocks[0])
	if chunkCount == 0 {
		return fmt.Errorf("empty encoded block")
	}
	relationCount := len(encoded.Matrix) * chunkCount
	if encoded.Aggregate == nil && (len(encoded.Proof.Rows) < relationCount ||
		len(encoded.Proof.Rows) != nextPowerOfTwo(len(encoded.Proof.Rows))) {
		return fmt.Errorf("proof coordinate row count mismatch")
	}
	for row := range encoded.Matrix {
		if len(encoded.Matrix[row]) != int(encoded.Layout.SourceCount) {
			return fmt.Errorf("matrix row %d source count mismatch", row)
		}
		if len(encoded.Blocks[row]) != chunkCount {
			return fmt.Errorf("block row %d chunk count mismatch", row)
		}
		for chunk := 0; chunk < chunkCount; chunk++ {
			index := row*chunkCount + chunk
			if encoded.Aggregate == nil {
				expectedA, err := expandCoefficientChunkRow(
					encoded.Matrix[row],
					chunkCount,
					chunk,
					len(encoded.Proof.Rows[index].A),
				)
				if err != nil {
					return err
				}
				if !scalarSlicesEqual(expectedA, encoded.Proof.Rows[index].A) {
					return fmt.Errorf("proof row %d chunk %d does not match generation matrix", row, chunk)
				}
				if !encoded.Blocks[row][chunk].Equal(&encoded.Proof.Rows[index].C) {
					return fmt.Errorf("encoded block row %d chunk %d does not match proof C", row, chunk)
				}
				continue
			}
			index += int(encoded.AggregateStart)
			if index >= len(encoded.Aggregate.Relations) || index >= len(encoded.Aggregate.Proof.Rows) {
				return fmt.Errorf("aggregate path row %d chunk %d is out of range", row, chunk)
			}
			relation := encoded.Aggregate.Relations[index]
			if relation.Kind != AggregateRelationPath || relation.Root != encoded.Root ||
				string(relation.Key) != string(encoded.Key) || relation.Row != uint64(row) {
				return fmt.Errorf("aggregate path relation %d identity mismatch", index)
			}
			localA, err := expandCoefficientChunkRow(
				encoded.Matrix[row],
				chunkCount,
				chunk,
				int(relation.Length),
			)
			if err != nil {
				return err
			}
			if !liftedRowMatches(localA, encoded.Aggregate.Proof.Rows[index].A, relation.Offset) {
				return fmt.Errorf("aggregate path row %d chunk %d coefficients mismatch", row, chunk)
			}
			if !encoded.Blocks[row][chunk].Equal(&relation.C) ||
				!encoded.Blocks[row][chunk].Equal(&encoded.Aggregate.Proof.Rows[index].C) {
				return fmt.Errorf("encoded block row %d chunk %d does not match aggregate C", row, chunk)
			}
		}
	}
	return nil
}

func validateHistoricalLeaf(leaf *HistoricalLeaf) error {
	if leaf == nil {
		return fmt.Errorf("nil historical leaf")
	}
	if err := validateLayout(leaf.Layout); err != nil {
		return err
	}
	if leaf.Proof == nil && leaf.Aggregate == nil {
		return fmt.Errorf("nil historical leaf proof")
	}
	if len(leaf.Selector) == 0 {
		return fmt.Errorf("historical selector length mismatch")
	}
	if len(leaf.Block) == 0 {
		return fmt.Errorf("empty historical leaf block")
	}
	framed, err := frameCompactLeaf(leaf.Leaf)
	if err != nil {
		return fmt.Errorf("frame historical leaf: %w", err)
	}
	expectedBlock, err := fileipa.BytesToFrChunks(framed)
	if err != nil {
		return fmt.Errorf("encode historical leaf: %w", err)
	}
	if !scalarSlicesEqual(leaf.Block, expectedBlock) {
		return fmt.Errorf("historical leaf block does not match raw leaf")
	}
	expectedSelector, err := historicalSelector(leaf.Layout, len(leaf.Block), len(leaf.Selector))
	if err != nil {
		return err
	}
	if !scalarSlicesEqual(leaf.Selector, expectedSelector) {
		return fmt.Errorf("historical proof row does not match selector")
	}
	if leaf.Aggregate == nil {
		if len(leaf.Proof.Rows) < len(leaf.Block) ||
			len(leaf.Proof.Rows) != nextPowerOfTwo(len(leaf.Proof.Rows)) {
			return fmt.Errorf("historical proof coordinate row count mismatch")
		}
		selectStart := uint64(int(leaf.Layout.CompactBytes)/SourceChunkSize - len(leaf.Block))
		for chunk := range leaf.Block {
			expectedA, err := historicalChunkSelector(selectStart, chunk, len(leaf.Proof.Rows[chunk].A))
			if err != nil {
				return err
			}
			if !scalarSlicesEqual(expectedA, leaf.Proof.Rows[chunk].A) {
				return fmt.Errorf("historical proof chunk %d selector mismatch", chunk)
			}
			if !leaf.Block[chunk].Equal(&leaf.Proof.Rows[chunk].C) {
				return fmt.Errorf("historical leaf chunk %d does not match proof C", chunk)
			}
		}
		return nil
	}
	for chunk := range leaf.Block {
		index := int(leaf.AggregateIndex) + chunk
		if index >= len(leaf.Aggregate.Relations) || index >= len(leaf.Aggregate.Proof.Rows) {
			return fmt.Errorf("aggregate historical chunk %d relation is out of range", chunk)
		}
		relation := leaf.Aggregate.Relations[index]
		if relation.Kind != AggregateRelationHistoricalLeaf || relation.Root != leaf.TerminalRoot ||
			string(relation.Key) != string(leaf.Key) {
			return fmt.Errorf("aggregate historical chunk %d identity mismatch", chunk)
		}
		localA, err := historicalChunkSelector(relation.SelectStart, chunk, int(relation.Length))
		if err != nil {
			return err
		}
		if !liftedRowMatches(localA, leaf.Aggregate.Proof.Rows[index].A, relation.Offset) {
			return fmt.Errorf("aggregate historical chunk %d selector mismatch", chunk)
		}
		if !leaf.Block[chunk].Equal(&relation.C) ||
			!leaf.Block[chunk].Equal(&leaf.Aggregate.Proof.Rows[index].C) {
			return fmt.Errorf("historical leaf chunk %d does not match aggregate C", chunk)
		}
	}
	return nil
}

func liftedRowMatches(local, lifted []fr.Element, offset uint64) bool {
	if offset > uint64(len(lifted)) || uint64(len(local)) > uint64(len(lifted))-offset {
		return false
	}
	for i := range lifted {
		if uint64(i) >= offset && uint64(i) < offset+uint64(len(local)) {
			if !lifted[i].Equal(&local[int(uint64(i)-offset)]) {
				return false
			}
		} else if !lifted[i].IsZero() {
			return false
		}
	}
	return true
}

func scalarSlicesEqual(left, right []fr.Element) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !left[i].Equal(&right[i]) {
			return false
		}
	}
	return true
}

func cloneMatrix(in [][]fr.Element) [][]fr.Element {
	out := make([][]fr.Element, len(in))
	for i := range in {
		out[i] = append([]fr.Element(nil), in[i]...)
	}
	return out
}

func validateLayout(layout PathLayout) error {
	if layout.NodeCount == 0 {
		return fmt.Errorf("empty path layout")
	}
	if layout.SourceCount == 0 {
		return fmt.Errorf("empty source layout")
	}
	if layout.FileSize != SourceSymbolSize {
		return fmt.Errorf("invalid layout file size %d", layout.FileSize)
	}
	if layout.CompactBytes < SourceChunkSize ||
		layout.CompactBytes%SourceChunkSize != 0 ||
		layout.CompactBytes > layout.SourceCount*layout.FileSize ||
		layout.CompactBytes <= (layout.SourceCount-1)*layout.FileSize {
		return fmt.Errorf("invalid compact path length %d", layout.CompactBytes)
	}
	return nil
}
