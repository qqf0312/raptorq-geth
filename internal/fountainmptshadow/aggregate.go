package fountainmptshadow

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/folding"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

type aggregateSegment struct {
	path  *EncodedPath
	leaf  *HistoricalLeaf
	files [][]byte
}

func buildEpochAggregate(height uint64, segments []aggregateSegment) (*EpochAggregate, error) {
	if len(segments) == 0 {
		return nil, nil
	}
	segmentWitnesses := make([][]fr.Element, len(segments))
	totalChunks := 0
	for i := range segments {
		witness, err := semanticWitness(segments[i].files)
		if err != nil {
			return nil, fmt.Errorf("aggregate segment %d witness: %w", i, err)
		}
		segmentWitnesses[i] = witness
		totalChunks += len(witness)
	}
	vectorLength := nextPowerOfTwo(totalChunks)
	globalWitness := make([]fr.Element, vectorLength)
	params, err := ipa.NewTestParams(vectorLength)
	if err != nil {
		return nil, fmt.Errorf("create epoch aggregate IPA params: %w", err)
	}

	var (
		relations          []AggregateRelation
		statements         []folding.Statement
		segmentQs          []bn254.G1Affine
		segmentCommitments []AggregateSegmentCommitment
		offset             int
	)
	for i := range segments {
		segment := segments[i]
		witness := segmentWitnesses[i]
		copy(globalWitness[offset:], witness)

		segmentVector := make([]fr.Element, vectorLength)
		copy(segmentVector[offset:], witness)
		segmentQ, err := ipa.CommitB(params, segmentVector)
		if err != nil {
			return nil, fmt.Errorf("commit aggregate segment %d: %w", i, err)
		}
		segmentQs = append(segmentQs, segmentQ)
		segmentCommitment := AggregateSegmentCommitment{Offset: uint64(offset), Q: segmentQ}

		switch {
		case segment.path != nil:
			segment.path.Q = segmentQ
			segment.path.AggregateStart = uint64(len(relations))
			for row := range segment.path.Matrix {
				for chunk := range segment.path.Blocks[row] {
					localA, err := expandCoefficientChunkRow(
						segment.path.Matrix[row],
						len(segment.path.Blocks[row]),
						chunk,
						len(witness),
					)
					if err != nil {
						return nil, fmt.Errorf("expand aggregate path row %d chunk %d: %w", row, chunk, err)
					}
					a := make([]fr.Element, vectorLength)
					copy(a[offset:], localA)
					c := segment.path.Blocks[row][chunk]
					relations = append(relations, AggregateRelation{
						Kind:         AggregateRelationPath,
						Root:         segment.path.Root,
						Key:          append([]byte(nil), segment.path.Key...),
						NodeCount:    segment.path.Layout.NodeCount,
						CompactBytes: segment.path.Layout.CompactBytes,
						Row:          uint64(row),
						Offset:       uint64(offset),
						Length:       uint64(len(witness)),
						C:            c,
						Seed:         append([]byte(nil), segment.path.Seed...),
						SourceCount:  segment.path.Layout.SourceCount,
						RowCount:     uint64(len(segment.path.Matrix)),
					})
					statements = append(statements, folding.Statement{A: a, C: c})
				}
			}
		case segment.leaf != nil:
			segment.leaf.Q = segmentQ
			segment.leaf.AggregateIndex = uint64(len(relations))
			selectStart := uint64(int(segment.leaf.Layout.CompactBytes)/SourceChunkSize - len(segment.leaf.Block))
			for chunk := range segment.leaf.Block {
				localA, err := historicalChunkSelector(selectStart, chunk, len(witness))
				if err != nil {
					return nil, fmt.Errorf("expand aggregate historical selector chunk %d: %w", chunk, err)
				}
				a := make([]fr.Element, vectorLength)
				copy(a[offset:], localA)
				c := segment.leaf.Block[chunk]
				relations = append(relations, AggregateRelation{
					Kind:           AggregateRelationHistoricalLeaf,
					Root:           segment.leaf.TerminalRoot,
					Key:            append([]byte(nil), segment.leaf.Key...),
					NodeCount:      segment.leaf.Layout.NodeCount,
					CompactBytes:   segment.leaf.Layout.CompactBytes,
					StartHeight:    segment.leaf.StartHeight,
					TerminalHeight: segment.leaf.TerminalHeight,
					Offset:         uint64(offset),
					Length:         uint64(len(witness)),
					C:              c,
					SelectStart:    selectStart,
					SelectCount:    uint64(len(segment.leaf.Block)),
				})
				statements = append(statements, folding.Statement{A: a, C: c})
			}
		default:
			return nil, fmt.Errorf("aggregate segment %d has no record", i)
		}
		segmentCommitments = append(segmentCommitments, segmentCommitment)
		offset += len(witness)
	}
	globalQ := sumG1(segmentQs)
	directQ, err := ipa.CommitB(params, globalWitness)
	if err != nil {
		return nil, fmt.Errorf("commit aggregate witness: %w", err)
	}
	if !globalQ.Equal(&directQ) {
		return nil, fmt.Errorf("summed segment commitments do not match aggregate commitment")
	}
	for i := range statements {
		statements[i].Q = globalQ
	}
	for len(statements) < nextPowerOfTwo(len(statements)) {
		statements = append(statements, folding.Statement{
			A: make([]fr.Element, vectorLength),
			Q: globalQ,
		})
	}
	root, foldProof, err := folding.FoldStatements(statements, epochAggregateDomain)
	if err != nil {
		return nil, fmt.Errorf("fold epoch aggregate: %w", err)
	}
	proof, qFromProof, cFromProof, err := ipa.Prove(params, root.A, globalWitness)
	if err != nil {
		return nil, fmt.Errorf("prove epoch aggregate: %w", err)
	}
	if !qFromProof.Equal(&globalQ) || !cFromProof.Equal(&root.C) {
		return nil, fmt.Errorf("epoch aggregate IPA statement mismatch")
	}
	rows := make([]fileipa.FileIPARow, len(statements))
	for i := range statements {
		rows[i] = fileipa.FileIPARow{
			A: append([]fr.Element(nil), statements[i].A...),
			C: statements[i].C,
		}
	}
	challenges := make([][]fr.Element, len(foldProof.Challenges))
	for i := range foldProof.Challenges {
		challenges[i] = append([]fr.Element(nil), foldProof.Challenges[i]...)
	}
	aggregate := &EpochAggregate{
		Height:    height,
		Relations: relations,
		Segments:  segmentCommitments,
		Proof: &fileipa.FoldedFileIPAResult{
			Params:    params,
			Rows:      rows,
			FoldProof: &fileipa.FileFoldProof{Challenges: challenges},
			IPAProof:  proof,
		},
	}
	for i := range segments {
		if segments[i].path != nil {
			segments[i].path.Proof = nil
			segments[i].path.Aggregate = aggregate
		} else {
			segments[i].leaf.Proof = nil
			segments[i].leaf.Aggregate = aggregate
		}
	}
	return aggregate, nil
}

func semanticWitness(files [][]byte) ([]fr.Element, error) {
	var witness []fr.Element
	for i := range files {
		chunks, err := fileipa.BytesToFrChunks(files[i])
		if err != nil {
			return nil, fmt.Errorf("source symbol %d: %w", i, err)
		}
		witness = append(witness, chunks...)
	}
	if len(witness) == 0 {
		return nil, fmt.Errorf("empty segment witness")
	}
	return witness, nil
}

func sumG1(points []bn254.G1Affine) bn254.G1Affine {
	var accumulator bn254.G1Jac
	for i := range points {
		accumulator.AddMixed(&points[i])
	}
	var out bn254.G1Affine
	out.FromJacobian(&accumulator)
	return out
}

func VerifyEpochAggregate(aggregate *EpochAggregate) (bool, error) {
	if aggregate == nil || aggregate.Proof == nil {
		return false, fmt.Errorf("nil epoch aggregate proof")
	}
	if len(aggregate.Relations) == 0 {
		return false, fmt.Errorf("empty epoch aggregate")
	}
	segmentQs := make([]bn254.G1Affine, len(aggregate.Segments))
	for i := range aggregate.Segments {
		segmentQs[i] = aggregate.Segments[i].Q
	}
	return fileipa.VerifyFoldedFileIPAResult(aggregate.Proof, sumG1(segmentQs), epochAggregateDomain)
}
