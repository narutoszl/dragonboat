// Copyright 2017-2019 Lei Ni (nilei81@gmail.com)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package raft

import (
	"github.com/lni/dragonboat/internal/settings"
	pb "github.com/lni/dragonboat/raftpb"
)

var (
	entrySliceSize    = settings.Soft.InMemEntrySliceSize
	minEntrySliceSize = settings.Soft.MinEntrySliceFreeSize
)

// inMemory is a two stage in memory log storage struct to keep log entries
// that will be used by the raft protocol in immediate future.
type inMemory struct {
	snapshot    *pb.Snapshot
	entries     []pb.Entry
	markerIndex uint64
	savedTo     uint64
}

func newInMemory(lastIndex uint64) inMemory {
	return inMemory{
		markerIndex: lastIndex + 1,
		savedTo:     lastIndex,
	}
}

func (im *inMemory) checkMarkerIndex() {
	if len(im.entries) > 0 {
		if im.entries[0].Index != im.markerIndex {
			plog.Panicf("marker index %d, first index %d",
				im.markerIndex, im.entries[0].Index)
		}
	}
}

func (im *inMemory) getEntries(low uint64, high uint64) []pb.Entry {
	upperBound := im.markerIndex + uint64(len(im.entries))
	if low > high || low < im.markerIndex {
		plog.Panicf("invalid low value %d, high %d, marker index %d",
			low, high, im.markerIndex)
	}
	if high > upperBound {
		plog.Panicf("invalid high value %d, upperBound %d", high, upperBound)
	}
	return im.entries[low-im.markerIndex : high-im.markerIndex]
}

func (im *inMemory) getSnapshotIndex() (uint64, bool) {
	if im.snapshot != nil {
		return im.snapshot.Index, true
	}
	return 0, false
}

func (im *inMemory) getLastIndex() (uint64, bool) {
	if len(im.entries) > 0 {
		return im.entries[len(im.entries)-1].Index, true
	}
	return im.getSnapshotIndex()
}

func (im *inMemory) getTerm(index uint64) (uint64, bool) {
	if index < im.markerIndex {
		if idx, ok := im.getSnapshotIndex(); ok && idx == index {
			return im.snapshot.Term, true
		}
		return 0, false
	}
	lastIndex, ok := im.getLastIndex()
	if ok && index <= lastIndex {
		return im.entries[index-im.markerIndex].Term, true
	}
	return 0, false
}

func (im *inMemory) commitUpdate(cu pb.UpdateCommit) {
	if cu.StableLogTo > 0 {
		im.savedLogTo(cu.StableLogTo, cu.StableLogTerm)
	}
	if cu.StableSnapshotTo > 0 {
		im.savedSnapshotTo(cu.StableSnapshotTo)
	}
}

func (im *inMemory) entriesToSave() []pb.Entry {
	idx := im.savedTo + 1
	if idx-im.markerIndex > uint64(len(im.entries)) {
		plog.Infof("nothing to save %+v", im)
		return []pb.Entry{}
	}
	return im.entries[idx-im.markerIndex:]
}

func (im *inMemory) savedLogTo(index uint64, term uint64) {
	if index < im.markerIndex {
		return
	}
	if len(im.entries) == 0 {
		return
	}
	if index > im.entries[len(im.entries)-1].Index ||
		term != im.entries[index-im.markerIndex].Term {
		return
	}
	im.savedTo = index
}

func (im *inMemory) appliedLogTo(index uint64) {
	if index < im.markerIndex {
		return
	}
	if len(im.entries) == 0 {
		return
	}
	if index > im.entries[len(im.entries)-1].Index {
		return
	}
	newMarkerIndex := index
	im.entries = im.entries[newMarkerIndex-im.markerIndex:]
	im.markerIndex = newMarkerIndex
	im.resizeEntrySlice()
	im.checkMarkerIndex()
}

func (im *inMemory) savedSnapshotTo(index uint64) {
	if idx, ok := im.getSnapshotIndex(); ok && idx == index {
		im.snapshot = nil
	} else if ok && idx != index {
		plog.Warningf("snapshot index does not match")
	}
}

func (im *inMemory) resizeEntrySlice() {
	if cap(im.entries)-len(im.entries) < int(minEntrySliceSize) {
		old := im.entries
		im.entries = make([]pb.Entry, 0, entrySliceSize)
		im.entries = append(im.entries, old...)
	}
}

func (im *inMemory) merge(ents []pb.Entry) {
	firstNewIndex := ents[0].Index
	im.resizeEntrySlice()
	if firstNewIndex == im.markerIndex+uint64(len(im.entries)) {
		checkEntriesToAppend(im.entries, ents)
		im.entries = append(im.entries, ents...)
	} else if firstNewIndex <= im.markerIndex {
		im.markerIndex = firstNewIndex
		// ents might come from entryQueue, copy it to its own storage
		im.entries = newEntrySlice(ents)
		im.savedTo = firstNewIndex - 1
	} else {
		existing := im.getEntries(im.markerIndex, firstNewIndex)
		checkEntriesToAppend(existing, ents)
		im.entries = make([]pb.Entry, 0, len(existing)+len(ents))
		im.entries = append(im.entries, existing...)
		im.entries = append(im.entries, ents...)
		im.savedTo = min(im.savedTo, firstNewIndex-1)
	}
	im.checkMarkerIndex()
}

func (im *inMemory) restore(ss pb.Snapshot) {
	im.snapshot = &ss
	im.markerIndex = ss.Index + 1
	im.entries = nil
	im.savedTo = ss.Index
}
