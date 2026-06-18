package mptproofmsg

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

const (
	ScalarWireSize = fr.Bytes
	ParamsIDTest   = "ipa-test-params-v1"
)

type ScalarWire []byte
type G1Wire []byte

type FileIPARowWire struct {
	A []ScalarWire
	C ScalarWire
}

type IPAProofWire struct {
	L      []G1Wire
	R      []G1Wire
	AFinal ScalarWire
	BFinal ScalarWire
}

func ScalarToWire(s fr.Element) ScalarWire {
	raw := s.Bytes()
	return append([]byte(nil), raw[:]...)
}

func ScalarFromWire(w ScalarWire) (fr.Element, error) {
	if len(w) != ScalarWireSize {
		return fr.Element{}, fmt.Errorf("invalid scalar wire length %d", len(w))
	}
	var out fr.Element
	if err := out.SetBytesCanonical(w); err != nil {
		return fr.Element{}, err
	}
	return out, nil
}

func G1ToWire(p bn254.G1Affine) G1Wire {
	raw := p.Bytes()
	return append([]byte(nil), raw[:]...)
}

func G1FromWire(w G1Wire) (bn254.G1Affine, error) {
	var out bn254.G1Affine
	if _, err := out.SetBytes(w); err != nil {
		return bn254.G1Affine{}, err
	}
	return out, nil
}

func FileIPARowToWire(row fileipa.FileIPARow) FileIPARowWire {
	return FileIPARowWire{
		A: ScalarsToWire(row.A),
		C: ScalarToWire(row.C),
	}
}

func FileIPARowFromWire(row FileIPARowWire) (fileipa.FileIPARow, error) {
	a, err := ScalarsFromWire(row.A)
	if err != nil {
		return fileipa.FileIPARow{}, err
	}
	c, err := ScalarFromWire(row.C)
	if err != nil {
		return fileipa.FileIPARow{}, err
	}
	return fileipa.FileIPARow{A: a, C: c}, nil
}

func IPAProofToWire(proof *ipa.Proof) (IPAProofWire, error) {
	if proof == nil {
		return IPAProofWire{}, fmt.Errorf("nil IPA proof")
	}
	return IPAProofWire{
		L:      G1SliceToWire(proof.L),
		R:      G1SliceToWire(proof.R),
		AFinal: ScalarToWire(proof.AFinal),
		BFinal: ScalarToWire(proof.BFinal),
	}, nil
}

func IPAProofFromWire(proof IPAProofWire) (*ipa.Proof, error) {
	l, err := G1SliceFromWire(proof.L)
	if err != nil {
		return nil, err
	}
	r, err := G1SliceFromWire(proof.R)
	if err != nil {
		return nil, err
	}
	aFinal, err := ScalarFromWire(proof.AFinal)
	if err != nil {
		return nil, err
	}
	bFinal, err := ScalarFromWire(proof.BFinal)
	if err != nil {
		return nil, err
	}
	return &ipa.Proof{
		L:      l,
		R:      r,
		AFinal: aFinal,
		BFinal: bFinal,
	}, nil
}

func NewFoldedFileIPAProofPacket(id uint64, root common.Hash, files []FileRefWire, sharedQ bn254.G1Affine, domain string, result *fileipa.FoldedFileIPAResult) (*FoldedFileIPAProofPacket, error) {
	if result == nil {
		return nil, fmt.Errorf("nil folded file IPA result")
	}
	if result.Params == nil {
		return nil, fmt.Errorf("nil folded file IPA params")
	}
	proof, err := IPAProofToWire(result.IPAProof)
	if err != nil {
		return nil, err
	}
	rows := make([]FileIPARowWire, len(result.Rows))
	for i := range result.Rows {
		rows[i] = FileIPARowToWire(result.Rows[i])
	}
	var challenges [][]ScalarWire
	if result.FoldProof != nil {
		challenges = ScalarMatrixToWire(result.FoldProof.Challenges)
	}
	return &FoldedFileIPAProofPacket{
		ID:             id,
		Root:           root,
		Files:          cloneFileRefs(files),
		Rows:           rows,
		FoldChallenges: challenges,
		IPAProof:       proof,
		SharedQ:        G1ToWire(sharedQ),
		VectorLength:   uint64(len(result.Params.G)),
		ParamsID:       ParamsIDTest,
		Domain:         domain,
	}, nil
}

func (p *FoldedFileIPAProofPacket) ToFileIPAResult() (*fileipa.FoldedFileIPAResult, bn254.G1Affine, string, error) {
	if p == nil {
		return nil, bn254.G1Affine{}, "", fmt.Errorf("nil folded file IPA proof packet")
	}
	if p.VectorLength == 0 {
		return nil, bn254.G1Affine{}, "", fmt.Errorf("empty vector length")
	}
	if p.ParamsID != "" && p.ParamsID != ParamsIDTest {
		return nil, bn254.G1Affine{}, "", fmt.Errorf("unsupported params id %q", p.ParamsID)
	}
	params, err := ipa.NewTestParams(int(p.VectorLength))
	if err != nil {
		return nil, bn254.G1Affine{}, "", err
	}
	rows := make([]fileipa.FileIPARow, len(p.Rows))
	for i := range p.Rows {
		rows[i], err = FileIPARowFromWire(p.Rows[i])
		if err != nil {
			return nil, bn254.G1Affine{}, "", fmt.Errorf("row %d: %w", i, err)
		}
	}
	challenges, err := ScalarMatrixFromWire(p.FoldChallenges)
	if err != nil {
		return nil, bn254.G1Affine{}, "", fmt.Errorf("fold challenges: %w", err)
	}
	proof, err := IPAProofFromWire(p.IPAProof)
	if err != nil {
		return nil, bn254.G1Affine{}, "", fmt.Errorf("IPA proof: %w", err)
	}
	sharedQ, err := G1FromWire(p.SharedQ)
	if err != nil {
		return nil, bn254.G1Affine{}, "", fmt.Errorf("shared Q: %w", err)
	}
	return &fileipa.FoldedFileIPAResult{
		Params: params,
		Rows:   rows,
		FoldProof: &fileipa.FileFoldProof{
			Challenges: challenges,
		},
		IPAProof: proof,
	}, sharedQ, p.Domain, nil
}

func VerifyFoldedFileIPAProofPacket(packet *FoldedFileIPAProofPacket) (bool, error) {
	result, sharedQ, domain, err := packet.ToFileIPAResult()
	if err != nil {
		return false, err
	}
	return fileipa.VerifyFoldedFileIPAResult(result, sharedQ, domain)
}

func ScalarsToWire(in []fr.Element) []ScalarWire {
	out := make([]ScalarWire, len(in))
	for i := range in {
		out[i] = ScalarToWire(in[i])
	}
	return out
}

func ScalarsFromWire(in []ScalarWire) ([]fr.Element, error) {
	out := make([]fr.Element, len(in))
	for i := range in {
		scalar, err := ScalarFromWire(in[i])
		if err != nil {
			return nil, fmt.Errorf("scalar %d: %w", i, err)
		}
		out[i] = scalar
	}
	return out, nil
}

func ScalarMatrixToWire(in [][]fr.Element) [][]ScalarWire {
	out := make([][]ScalarWire, len(in))
	for i := range in {
		out[i] = ScalarsToWire(in[i])
	}
	return out
}

func ScalarMatrixFromWire(in [][]ScalarWire) ([][]fr.Element, error) {
	out := make([][]fr.Element, len(in))
	for i := range in {
		row, err := ScalarsFromWire(in[i])
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i, err)
		}
		out[i] = row
	}
	return out, nil
}

func G1SliceToWire(in []bn254.G1Affine) []G1Wire {
	out := make([]G1Wire, len(in))
	for i := range in {
		out[i] = G1ToWire(in[i])
	}
	return out
}

func G1SliceFromWire(in []G1Wire) ([]bn254.G1Affine, error) {
	out := make([]bn254.G1Affine, len(in))
	for i := range in {
		point, err := G1FromWire(in[i])
		if err != nil {
			return nil, fmt.Errorf("point %d: %w", i, err)
		}
		out[i] = point
	}
	return out, nil
}

func cloneFileRefs(in []FileRefWire) []FileRefWire {
	out := make([]FileRefWire, len(in))
	for i := range in {
		out[i] = FileRefWire{
			Key:  append([]byte(nil), in[i].Key...),
			Hash: in[i].Hash,
			Size: in[i].Size,
		}
	}
	return out
}
