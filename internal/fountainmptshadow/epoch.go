package fountainmptshadow

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
)

// EpochTracker records every root and derives terminal-height version records.
// It stores no copied path witness; callers resolve original paths from the
// retained roots before pruning.
type EpochTracker struct {
	start uint64
	roots map[uint64]common.Hash

	current    map[string]VersionRecord
	historical []VersionRecord
}

func NewEpochTracker(start uint64) *EpochTracker {
	return &EpochTracker{
		start:   start,
		roots:   make(map[uint64]common.Hash),
		current: make(map[string]VersionRecord),
	}
}

// KeyUpdate records one new key version. PreviousExists reports whether the
// key had a leaf in root_(height-1).
type KeyUpdate struct {
	Key            []byte
	PreviousExists bool
	NewExists      bool
}

// RecordHeight records the root for one height and the keys whose leaf version
// changed at that height.
func (t *EpochTracker) RecordHeight(height uint64, root common.Hash, updates []KeyUpdate) error {
	return t.RecordHeightWithParent(height, root, common.Hash{}, updates)
}

// RecordHeightWithParent additionally supplies root_(height-1), allowing a
// version already alive before the epoch to terminate at the epoch's first
// height without copying its path.
func (t *EpochTracker) RecordHeightWithParent(height uint64, root common.Hash, parent common.Hash, updates []KeyUpdate) error {
	if t == nil {
		return fmt.Errorf("nil epoch tracker")
	}
	if height < t.start {
		return fmt.Errorf("height %d precedes epoch start %d", height, t.start)
	}
	if _, exists := t.roots[height]; exists {
		return fmt.Errorf("height %d already recorded", height)
	}
	t.roots[height] = root
	for _, update := range updates {
		if len(update.Key) == 0 {
			return fmt.Errorf("empty updated key")
		}
		id := string(update.Key)
		if previous, ok := t.current[id]; ok {
			terminalRoot, err := t.terminalRoot(height, parent)
			if err != nil {
				return err
			}
			previous.TerminalHeight = height - 1
			previous.TerminalRoot = terminalRoot
			t.historical = append(t.historical, previous)
		} else if update.PreviousExists {
			terminalRoot, err := t.terminalRoot(height, parent)
			if err != nil {
				return err
			}
			startHeight := t.start
			if height == t.start && startHeight > 0 {
				// The version is carried into this epoch from root_(start-1).
				startHeight--
			}
			t.historical = append(t.historical, VersionRecord{
				Key:            append([]byte(nil), update.Key...),
				StartHeight:    startHeight,
				TerminalHeight: height - 1,
				TerminalRoot:   terminalRoot,
			})
		}
		if update.NewExists {
			t.current[id] = VersionRecord{
				Key:         append([]byte(nil), update.Key...),
				StartHeight: height,
			}
		} else {
			delete(t.current, id)
		}
	}
	return nil
}

// Finalize returns all historical intervals plus versions still active at the
// final height. It does not mutate the tracker.
func (t *EpochTracker) Finalize(finalHeight uint64) ([]RootRecord, []VersionRecord, error) {
	if t == nil {
		return nil, nil, fmt.Errorf("nil epoch tracker")
	}
	finalRoot, ok := t.roots[finalHeight]
	if !ok {
		return nil, nil, fmt.Errorf("missing final root at height %d", finalHeight)
	}
	roots := make([]RootRecord, 0, len(t.roots))
	for height, root := range t.roots {
		if height <= finalHeight {
			roots = append(roots, RootRecord{Height: height, Root: root})
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Height < roots[j].Height })

	versions := cloneVersions(t.historical)
	for _, current := range t.current {
		current.TerminalHeight = finalHeight
		current.TerminalRoot = finalRoot
		current.Final = true
		versions = append(versions, current)
	}
	sort.Slice(versions, func(i, j int) bool {
		if cmp := bytes.Compare(versions[i].Key, versions[j].Key); cmp != 0 {
			return cmp < 0
		}
		return versions[i].StartHeight < versions[j].StartHeight
	})
	return roots, versions, nil
}

func (t *EpochTracker) terminalRoot(updateHeight uint64, parent common.Hash) (common.Hash, error) {
	if updateHeight == 0 {
		return common.Hash{}, fmt.Errorf("height zero has no previous root")
	}
	root, ok := t.roots[updateHeight-1]
	if ok {
		return root, nil
	}
	if parent != (common.Hash{}) {
		return parent, nil
	}
	return common.Hash{}, fmt.Errorf("missing terminal root at height %d", updateHeight-1)
}

func cloneVersions(in []VersionRecord) []VersionRecord {
	out := make([]VersionRecord, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Key = append([]byte(nil), in[i].Key...)
	}
	return out
}
