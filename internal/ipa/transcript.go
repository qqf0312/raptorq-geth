package ipa

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// ChallengeScalar derives the Fiat-Shamir challenge for one IPA round. Prove
// and Verify call this with identical domain, round, current P, L and R.
func ChallengeScalar(domain string, round int, P, L, R bn254.G1Affine) (fr.Element, error) {
	if round < 0 {
		return fr.Element{}, fmt.Errorf("negative round")
	}
	for counter := uint32(0); ; counter++ {
		h := sha256.New()
		h.Write([]byte(domain))
		writeUint64(h, uint64(round))
		writePoint(h, P)
		writePoint(h, L)
		writePoint(h, R)
		writeUint64(h, uint64(counter))

		var challenge fr.Element
		challenge.SetBytes(h.Sum(nil))
		if !challenge.IsZero() {
			return challenge, nil
		}
		if counter == ^uint32(0) {
			return fr.Element{}, fmt.Errorf("failed to derive non-zero challenge")
		}
	}
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeUint64(w hashWriter, v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	w.Write(buf[:])
}

func writePoint(w hashWriter, p bn254.G1Affine) {
	raw := p.RawBytes()
	w.Write(raw[:])
}
