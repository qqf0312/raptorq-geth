package mptagg

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

func BuildElementsWithPositionPrefix(nodes []MPTNodeLike, startPosition int) ([]fr.Element, int, error) {
	if startPosition < 0 {
		return nil, 0, fmt.Errorf("negative start position %d", startPosition)
	}
	canonical, err := CanonicalPathBytes(nodes)
	if err != nil {
		return nil, 0, err
	}
	chunks, err := BytesToFrChunks(canonical)
	if err != nil {
		return nil, 0, err
	}
	if startPosition == 0 {
		return padElementsToPowerOfTwo(chunks), 0, nil
	}
	elements := make([]fr.Element, startPosition, startPosition+len(chunks))
	elements = append(elements, chunks...)
	return padElementsToPowerOfTwo(elements), startPosition, nil
}

func CommitPathWithPositionPrefix(params *CommitmentParams, nodes []MPTNodeLike, startPosition int) (bn254.G1Affine, []fr.Element, int, error) {
	elements, prefixZeros, err := BuildElementsWithPositionPrefix(nodes, startPosition)
	if err != nil {
		return bn254.G1Affine{}, nil, 0, err
	}
	commitment, err := ipa.CommitB(params, elements)
	if err != nil {
		return bn254.G1Affine{}, nil, 0, fmt.Errorf("commit MPT segment with position prefix: %w", err)
	}
	return commitment, elements, prefixZeros, nil
}
