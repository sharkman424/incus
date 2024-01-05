package idmap

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/lxc/incus/shared/util"
)

// ErrHostIDIsSubID indicates that an expected host ID is part of a subid range.
var ErrHostIDIsSubID = fmt.Errorf("Host ID is in the range of subids")

// IdmapSet is a list of Entry with some functions on it.
type IdmapSet struct {
	Idmap []Entry
}

// Equals checks if two IdmapSet are functionally identical.
func (m *IdmapSet) Equals(other *IdmapSet) bool {
	// Get comparable maps.
	expandSortIdmap := func(input *IdmapSet) *IdmapSet {
		if input == nil {
			input = &IdmapSet{}
		}

		newEntries := []Entry{}

		for _, entry := range input.Idmap {
			if entry.Isuid && entry.Isgid {
				newEntries = append(newEntries, Entry{true, false, entry.Hostid, entry.Nsid, entry.Maprange})
				newEntries = append(newEntries, Entry{false, true, entry.Hostid, entry.Nsid, entry.Maprange})
			} else {
				newEntries = append(newEntries, entry)
			}
		}

		output := &IdmapSet{Idmap: newEntries}
		sort.Sort(output)

		return output
	}

	// Actually compare.
	return reflect.DeepEqual(expandSortIdmap(m), expandSortIdmap(other))
}

// Len returns the number of Entry contained in the set.
func (m *IdmapSet) Len() int {
	return len(m.Idmap)
}

// Swap allows swapping two Entry in the set (used for sorting).
func (m *IdmapSet) Swap(i, j int) {
	m.Idmap[i], m.Idmap[j] = m.Idmap[j], m.Idmap[i]
}

// Less compares two Entry in the set (used for sorting).
func (m *IdmapSet) Less(i, j int) bool {
	if m.Idmap[i].Isuid != m.Idmap[j].Isuid {
		return m.Idmap[i].Isuid
	}

	if m.Idmap[i].Isgid != m.Idmap[j].Isgid {
		return m.Idmap[i].Isgid
	}

	return m.Idmap[i].Nsid < m.Idmap[j].Nsid
}

// Intersects checks if any of the Entry in the set intersects with the provided entry.
func (m *IdmapSet) Intersects(i Entry) bool {
	for _, e := range m.Idmap {
		if i.Intersects(e) {
			return true
		}
	}

	return false
}

// HostidsIntersect checks if any of the Entry hostids in the set intersects with the provided entry.
func (m *IdmapSet) HostidsIntersect(i Entry) bool {
	for _, e := range m.Idmap {
		if i.HostidsIntersect(e) {
			return true
		}
	}

	return false
}

// Usable checks that all Entry in the set are usable.
func (m *IdmapSet) Usable() error {
	for _, e := range m.Idmap {
		err := e.Usable()
		if err != nil {
			return err
		}
	}

	return nil
}

// ValidRanges turns the set into a slice of IDRange.
func (m *IdmapSet) ValidRanges() ([]*IDRange, error) {
	ranges := []*IDRange{}

	// Sort the map.
	idmap := &IdmapSet{}
	err := util.DeepCopy(&m, &idmap)
	if err != nil {
		return nil, err
	}

	sort.Sort(idmap)

	for _, mapEntry := range idmap.Idmap {
		var entry *IDRange

		for _, idEntry := range ranges {
			if mapEntry.Isuid != idEntry.Isuid || mapEntry.Isgid != idEntry.Isgid {
				continue
			}

			if idEntry.Endid+1 == mapEntry.Nsid {
				entry = idEntry
				break
			}
		}

		if entry != nil {
			entry.Endid = entry.Endid + mapEntry.Maprange
			continue
		}

		ranges = append(ranges, &IDRange{
			Isuid:   mapEntry.Isuid,
			Isgid:   mapEntry.Isgid,
			Startid: mapEntry.Nsid,
			Endid:   mapEntry.Nsid + mapEntry.Maprange - 1,
		})
	}

	return ranges, nil
}

// AddSafe adds an entry to the idmap set, breaking apart any ranges that the
// new idmap intersects with in the process.
func (m *IdmapSet) AddSafe(i Entry) error {
	result := []Entry{}
	added := false
	for _, e := range m.Idmap {
		if !e.Intersects(i) {
			result = append(result, e)
			continue
		}

		if e.HostidsIntersect(i) {
			return ErrHostIDIsSubID
		}

		added = true

		lower := Entry{
			Isuid:    e.Isuid,
			Isgid:    e.Isgid,
			Hostid:   e.Hostid,
			Nsid:     e.Nsid,
			Maprange: i.Nsid - e.Nsid,
		}

		upper := Entry{
			Isuid:    e.Isuid,
			Isgid:    e.Isgid,
			Hostid:   e.Hostid + lower.Maprange + i.Maprange,
			Nsid:     i.Nsid + i.Maprange,
			Maprange: e.Maprange - i.Maprange - lower.Maprange,
		}

		if lower.Maprange > 0 {
			result = append(result, lower)
		}

		result = append(result, i)
		if upper.Maprange > 0 {
			result = append(result, upper)
		}
	}

	if !added {
		result = append(result, i)
	}

	m.Idmap = result

	return nil
}

// ToLxcString converts the set to a slice of LXC configuration entries.
func (m *IdmapSet) ToLxcString() []string {
	var lines []string
	for _, e := range m.Idmap {
		for _, l := range e.ToLxcString() {
			if !util.ValueInSlice(l, lines) {
				lines = append(lines, l)
			}
		}
	}

	return lines
}

// Append adds an entry to the set.
func (m *IdmapSet) Append(s string) (*IdmapSet, error) {
	e := Entry{}

	err := e.parse(s)
	if err != nil {
		return m, err
	}

	if m.Intersects(e) {
		return m, fmt.Errorf("Conflicting id mapping")
	}

	m.Idmap = append(m.Idmap, e)
	return m, nil
}

func (m IdmapSet) doShiftIntoNs(uid int64, gid int64, how string) (int64, int64) {
	u := int64(-1)
	g := int64(-1)

	for _, e := range m.Idmap {
		var err error
		var tmpu, tmpg int64
		if e.Isuid && u == -1 {
			switch how {
			case "in":
				tmpu, err = e.shiftIntoNS(uid)
			case "out":
				tmpu, err = e.shiftFromNS(uid)
			}

			if err == nil {
				u = tmpu
			}
		}

		if e.Isgid && g == -1 {
			switch how {
			case "in":
				tmpg, err = e.shiftIntoNS(gid)
			case "out":
				tmpg, err = e.shiftFromNS(gid)
			}

			if err == nil {
				g = tmpg
			}
		}
	}

	return u, g
}

// ShiftIntoNs shiftfs the provided uid and gid into their container equivalent.
func (m IdmapSet) ShiftIntoNs(uid int64, gid int64) (int64, int64) {
	return m.doShiftIntoNs(uid, gid, "in")
}

// ShiftFromNs shiftfs the provided uid and gid into their host equivalent.
func (m IdmapSet) ShiftFromNs(uid int64, gid int64) (int64, int64) {
	return m.doShiftIntoNs(uid, gid, "out")
}

// JSONUnmarshal unmarshals an IDMAP encoded as JSON.
func JSONUnmarshal(idmapJSON string) (*IdmapSet, error) {
	lastIdmap := new(IdmapSet)
	err := json.Unmarshal([]byte(idmapJSON), &lastIdmap.Idmap)
	if err != nil {
		return nil, err
	}

	if len(lastIdmap.Idmap) == 0 {
		return nil, nil
	}

	return lastIdmap, nil
}

// JSONMarshal marshals an IDMAP to JSON string.
func JSONMarshal(idmapSet *IdmapSet) (string, error) {
	idmapBytes, err := json.Marshal(idmapSet.Idmap)
	if err != nil {
		return "", err
	}

	return string(idmapBytes), nil
}

// ByHostID allows for sorting an IdmapSet by host id.
type ByHostID IdmapSet

func (s ByHostID) Len() int {
	return len(s.Idmap)
}

func (s ByHostID) Swap(i, j int) {
	s.Idmap[i], s.Idmap[j] = s.Idmap[j], s.Idmap[i]
}

func (s ByHostID) Less(i, j int) bool {
	return s.Idmap[i].Hostid < s.Idmap[j].Hostid
}
