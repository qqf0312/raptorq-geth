package fountainmptshadow

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestEpochTrackerUsesTerminalRoots(t *testing.T) {
	tracker := NewEpochTracker(5)
	key := []byte("key-0")
	for height := uint64(5); height <= 10; height++ {
		var updates []KeyUpdate
		switch height {
		case 6:
			updates = []KeyUpdate{{Key: key, PreviousExists: true, NewExists: true}}
		case 9:
			updates = []KeyUpdate{{Key: key, PreviousExists: true, NewExists: true}}
		}
		if err := tracker.RecordHeight(height, rootForHeight(height), updates); err != nil {
			t.Fatalf("RecordHeight(%d): %v", height, err)
		}
	}
	roots, versions, err := tracker.Finalize(10)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(roots) != 6 {
		t.Fatalf("roots %d, want 6", len(roots))
	}
	if len(versions) != 3 {
		t.Fatalf("versions %d, want 3", len(versions))
	}
	assertVersion(t, versions[0], 5, 5, rootForHeight(5), false)
	assertVersion(t, versions[1], 6, 8, rootForHeight(8), false)
	assertVersion(t, versions[2], 9, 10, rootForHeight(10), true)
}

func TestEpochTrackerCarriesVersionAcrossEpochBoundary(t *testing.T) {
	tracker := NewEpochTracker(13)
	key := []byte("carried-key")
	if err := tracker.RecordHeightWithParent(13, rootForHeight(13), rootForHeight(12), []KeyUpdate{{
		Key:            key,
		PreviousExists: true,
		NewExists:      true,
	}}); err != nil {
		t.Fatalf("RecordHeightWithParent: %v", err)
	}
	_, versions, err := tracker.Finalize(13)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions %d, want 2", len(versions))
	}
	assertVersion(t, versions[0], 12, 12, rootForHeight(12), false)
	assertVersion(t, versions[1], 13, 13, rootForHeight(13), true)
}

func assertVersion(t *testing.T, got VersionRecord, start, terminal uint64, root common.Hash, final bool) {
	t.Helper()
	if got.StartHeight != start || got.TerminalHeight != terminal || got.TerminalRoot != root || got.Final != final {
		t.Fatalf("version got start=%d terminal=%d root=%s final=%v", got.StartHeight, got.TerminalHeight, got.TerminalRoot, got.Final)
	}
}

func rootForHeight(height uint64) common.Hash {
	return common.BigToHash(new(big.Int).SetUint64(height))
}
