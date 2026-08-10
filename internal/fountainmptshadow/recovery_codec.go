package fountainmptshadow

import (
	"bytes"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
)

const recoveryAggregateVersion = 0xb1

// EncodeRecoveryAggregate produces a self-contained network representation of
// the existing epoch aggregate. Unlike the disk codec it includes historical
// public C values, so a receiver does not need the sender's historical leaves
// to verify the unchanged aggregate proof.
func EncodeRecoveryAggregate(aggregate *EpochAggregate) ([]byte, error) {
	if aggregate == nil || aggregate.Proof == nil || aggregate.Proof.IPAProof == nil {
		return nil, fmt.Errorf("nil epoch aggregate")
	}
	if ok, err := VerifyEpochAggregate(aggregate); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("invalid epoch aggregate proof")
	}
	groups, err := compactAggregateRelations(aggregate.Relations)
	if err != nil {
		return nil, err
	}
	pathRoot, sharedPathRoot := aggregatePathRoot(aggregate.Relations)
	for i := range groups {
		group := &groups[i]
		if group.Kind != AggregateRelationPath {
			continue
		}
		if sharedPathRoot {
			group.Root = common.Hash{}
		}
		if len(aggregate.PathSeedBase) != 0 {
			seedRoot := group.Root
			if sharedPathRoot {
				seedRoot = pathRoot
			}
			if !bytes.Equal(group.Seed, derivePathSeed(aggregate.PathSeedBase, aggregate.Height, seedRoot, group.Key)) {
				return nil, fmt.Errorf("aggregate path seed is not derived from epoch seed")
			}
			group.Seed = nil
		}
	}
	proof, err := mptproofmsg.IPAProofToWire(aggregate.Proof.IPAProof)
	if err != nil {
		return nil, err
	}
	out := []byte{recoveryAggregateVersion}
	out = appendUint(out, aggregate.Height)
	if sharedPathRoot {
		out = append(out, pathRoot[:]...)
	} else {
		out = append(out, make([]byte, common.HashLength)...)
	}
	out = appendBytes(out, aggregate.PathSeedBase)
	out = appendUint(out, uint64(len(groups)))
	for _, group := range groups {
		out = append(out, group.Kind)
		out = append(out, group.Root[:]...)
		out = appendBytes(out, group.Key)
		out = appendBytes(out, group.Seed)
		for _, value := range []uint64{
			group.NodeCount, group.CompactBytes, group.StartHeight,
			group.TerminalHeight, group.Row, group.Offset, group.Length,
			group.SourceCount, group.RowCount, group.SelectStart, group.SelectCount,
		} {
			out = appendUint(out, value)
		}
	}
	out = appendUint(out, uint64(len(aggregate.Segments)))
	for _, segment := range aggregate.Segments {
		out = appendUint(out, segment.Offset)
		out = appendBytes(out, mptproofmsg.G1ToWire(segment.Q))
	}
	out = appendUint(out, uint64(len(aggregate.Relations)))
	for _, relation := range aggregate.Relations {
		out = appendBytes(out, mptproofmsg.ScalarToWire(relation.C))
	}
	out = appendWireSlice(out, proof.L)
	out = appendWireSlice(out, proof.R)
	out = appendBytes(out, proof.AFinal)
	out = appendBytes(out, proof.BFinal)
	return out, nil
}

// DecodeRecoveryAggregate reconstructs every public statement and verifies
// canonical ordering. Cryptographic verification remains an explicit caller
// step so callers can first bind the target segment Q to their local Q.
func DecodeRecoveryAggregate(blob []byte) (*EpochAggregate, error) {
	if len(blob) == 0 || blob[0] != recoveryAggregateVersion {
		return nil, fmt.Errorf("invalid recovery aggregate version")
	}
	r := compactReader{data: blob, off: 1}
	height, err := r.uint()
	if err != nil {
		return nil, err
	}
	pathRootBytes, err := r.fixed(common.HashLength)
	if err != nil {
		return nil, err
	}
	pathSeedBase, err := r.bytes()
	if err != nil {
		return nil, err
	}
	groupCount, err := r.uint()
	if err != nil {
		return nil, err
	}
	stored := storedEpochAggregate{
		Height:       height,
		PathRoot:     common.BytesToHash(pathRootBytes),
		PathSeedBase: pathSeedBase,
		Relations:    make([]storedAggregateRelation, int(groupCount)),
	}
	for i := range stored.Relations {
		kind, err := r.fixed(1)
		if err != nil {
			return nil, err
		}
		root, err := r.fixed(common.HashLength)
		if err != nil {
			return nil, err
		}
		key, err := r.bytes()
		if err != nil {
			return nil, err
		}
		seed, err := r.bytes()
		if err != nil {
			return nil, err
		}
		values := make([]uint64, 11)
		for j := range values {
			values[j], err = r.uint()
			if err != nil {
				return nil, err
			}
		}
		stored.Relations[i] = storedAggregateRelation{
			Kind:           kind[0],
			Root:           common.BytesToHash(root),
			Key:            key,
			Seed:           seed,
			NodeCount:      values[0],
			CompactBytes:   values[1],
			StartHeight:    values[2],
			TerminalHeight: values[3],
			Row:            values[4],
			Offset:         values[5],
			Length:         values[6],
			SourceCount:    values[7],
			RowCount:       values[8],
			SelectStart:    values[9],
			SelectCount:    values[10],
		}
	}
	segmentCount, err := r.uint()
	if err != nil {
		return nil, err
	}
	stored.Segments = make([]storedAggregateSegmentCommitment, int(segmentCount))
	for i := range stored.Segments {
		offset, err := r.uint()
		if err != nil {
			return nil, err
		}
		q, err := r.bytes()
		if err != nil {
			return nil, err
		}
		stored.Segments[i] = storedAggregateSegmentCommitment{Offset: offset, Q: q}
	}
	cCount, err := r.uint()
	if err != nil {
		return nil, err
	}
	cs := make([]fr.Element, int(cCount))
	for i := range cs {
		wire, err := r.bytes()
		if err != nil {
			return nil, err
		}
		cs[i], err = mptproofmsg.ScalarFromWire(wire)
		if err != nil {
			return nil, err
		}
	}
	l, err := readWireSlice(&r)
	if err != nil {
		return nil, err
	}
	rPoints, err := readWireSlice(&r)
	if err != nil {
		return nil, err
	}
	aFinal, err := r.bytes()
	if err != nil {
		return nil, err
	}
	bFinal, err := r.bytes()
	if err != nil {
		return nil, err
	}
	if err := r.done(); err != nil {
		return nil, err
	}
	if err := normalizeStoredAggregate(&stored); err != nil {
		return nil, err
	}
	maxEnd := uint64(0)
	for _, group := range stored.Relations {
		if group.Length == 0 || group.Offset > ^uint64(0)-group.Length {
			return nil, fmt.Errorf("invalid recovery relation dimensions")
		}
		if end := group.Offset + group.Length; end > maxEnd {
			maxEnd = end
		}
	}
	vectorLength := nextPowerOfTwo(int(maxEnd))
	params, err := ipa.NewTestParams(vectorLength)
	if err != nil {
		return nil, err
	}
	relationCount := 0
	for _, group := range stored.Relations {
		switch group.Kind {
		case AggregateRelationPath:
			relationCount += SourceSymbolSize / SourceChunkSize
		case AggregateRelationHistoricalLeaf:
			relationCount += int(group.SelectCount)
		default:
			return nil, fmt.Errorf("unknown aggregate relation kind %d", group.Kind)
		}
	}
	if relationCount != len(cs) {
		return nil, fmt.Errorf("aggregate C count %d does not match relations %d", len(cs), relationCount)
	}
	segments := make([]AggregateSegmentCommitment, len(stored.Segments))
	for i, segment := range stored.Segments {
		q, err := mptproofmsg.G1FromWire(segment.Q)
		if err != nil {
			return nil, err
		}
		segments[i] = AggregateSegmentCommitment{Offset: segment.Offset, Q: q}
	}
	rows := make([]fileipa.FileIPARow, nextPowerOfTwo(relationCount))
	relations := make([]AggregateRelation, 0, relationCount)
	nextC := 0
	for groupIndex, group := range stored.Relations {
		chunkCount := SourceSymbolSize / SourceChunkSize
		if group.Kind == AggregateRelationHistoricalLeaf {
			chunkCount = int(group.SelectCount)
		}
		for chunk := 0; chunk < chunkCount; chunk++ {
			localA, err := storedAggregateRelationA(group, chunk)
			if err != nil {
				return nil, fmt.Errorf("recovery relation group %d chunk %d: %w", groupIndex, chunk, err)
			}
			if group.Offset+uint64(len(localA)) > uint64(vectorLength) {
				return nil, fmt.Errorf("recovery relation group %d exceeds witness", groupIndex)
			}
			a := make([]fr.Element, vectorLength)
			copy(a[group.Offset:], localA)
			c := cs[nextC]
			rows[nextC] = fileipa.FileIPARow{A: a, C: c}
			relations = append(relations, AggregateRelation{
				Kind: group.Kind, Root: group.Root, Key: append([]byte(nil), group.Key...),
				NodeCount: group.NodeCount, CompactBytes: group.CompactBytes,
				StartHeight: group.StartHeight, TerminalHeight: group.TerminalHeight,
				Row: group.Row, Offset: group.Offset, Length: group.Length, C: c,
				Seed: append([]byte(nil), group.Seed...), SourceCount: group.SourceCount,
				RowCount: group.RowCount, SelectStart: group.SelectStart, SelectCount: group.SelectCount,
			})
			nextC++
		}
	}
	for i := relationCount; i < len(rows); i++ {
		rows[i] = fileipa.FileIPARow{A: make([]fr.Element, vectorLength)}
	}
	q := sumG1(segmentPoints(segments))
	challenges, err := loadOrDeriveFoldChallenges(nil, rows, q, epochAggregateDomain)
	if err != nil {
		return nil, err
	}
	proof, err := mptproofmsg.IPAProofFromWire(mptproofmsg.IPAProofWire{
		L: l, R: rPoints, AFinal: aFinal, BFinal: bFinal,
	})
	if err != nil {
		return nil, err
	}
	return &EpochAggregate{
		Height: height, PathSeedBase: append([]byte(nil), pathSeedBase...),
		Relations: relations, Segments: segments,
		Proof: &fileipa.FoldedFileIPAResult{
			Params: params, Rows: rows,
			FoldProof: &fileipa.FileFoldProof{Challenges: challenges}, IPAProof: proof,
		},
	}, nil
}
