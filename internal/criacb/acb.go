// Package criacb models a CRI ACB container: it expands nested @UTF tables
// from the root row and maps cue names to AWB stream ids.
package criacb

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"cri_extract/internal/criutf"
)

// ACB is an opened ACB file.
type ACB struct {
	// Fields mirrors the single root row: field name -> *criutf.Val or *criutf.Table
	// (nested @UTF byte fields are expanded to tables recursively).
	Fields map[string]any
}

// Expand returns parsed nested tables for a cell value (nil if not a @UTF blob).
func expandVal(v *criutf.Val) (*criutf.Table, error) {
	if v == nil || v.Type != criutf.TypeBytes {
		return nil, nil
	}
	if !bytes.HasPrefix(v.B, []byte("@UTF")) {
		return nil, nil
	}
	t, err := criutf.Parse(v.B)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func expandRow(row map[string]*criutf.Val) (map[string]any, error) {
	out := make(map[string]any, len(row))
	for k, v := range row {
		t, err := expandVal(v)
		if err != nil {
			return nil, fmt.Errorf("expand %s: %w", k, err)
		}
		if t != nil {
			out[k] = t
		} else {
			out[k] = v
		}
	}
	return out, nil
}

// Open parses ACB bytes (raw @UTF file or standalone) with nested tables.
func Open(data []byte) (*ACB, error) {
	root, err := criutf.Parse(data)
	if err != nil {
		return nil, err
	}
	if len(root.Rows) == 0 {
		return nil, fmt.Errorf("acb: no rows")
	}
	fields, err := expandRow(root.Rows[0])
	if err != nil {
		return nil, err
	}
	return &ACB{Fields: fields}, nil
}

// Table returns a named nested table, or nil.
func (a *ACB) Table(name string) *criutf.Table {
	if t, ok := a.Fields[name].(*criutf.Table); ok {
		return t
	}
	return nil
}

// Val returns a named root scalar/bytes field, or nil.
func (a *ACB) Val(name string) *criutf.Val {
	if v, ok := a.Fields[name].(*criutf.Val); ok {
		return v
	}
	return nil
}

// Name is the human-readable ACB/cue base name.
func (a *ACB) Name() string {
	v := a.Val("Name")
	if v != nil && v.IsString() {
		return v.Str
	}
	return ""
}

// EmbeddedAWB returns in-container AWB bytes if present.
func (a *ACB) EmbeddedAWB() []byte {
	v := a.Val("AwbFile")
	if v != nil && v.IsBytes() && len(v.B) > 0 {
		return v.B
	}
	return nil
}

// StreamHash returns the 16-byte external AWB hash (StreamAwbHash row Hash).
func (a *ACB) StreamHash() []byte {
	t := a.Table("StreamAwbHash")
	if t == nil || len(t.Rows) == 0 {
		return nil
	}
	if v := t.Rows[0]["Hash"]; v != nil && v.IsBytes() && len(v.B) == 16 {
		return v.B
	}
	return nil
}

// CueNames returns all cue names in CueNameTable order.
func (a *ACB) CueNames() []string {
	t := a.Table("CueNameTable")
	if t == nil {
		return nil
	}
	var out []string
	for _, r := range t.Rows {
		if v := r["CueName"]; v != nil && v.IsString() {
			out = append(out, v.Str)
		}
	}
	return out
}

// tableRows returns rows of a nested table, or nil if absent.
func tableRows(t *criutf.Table) []map[string]*criutf.Val {
	if t == nil {
		return nil
	}
	return t.Rows
}

func valInt(v *criutf.Val) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch v.Type {
	case criutf.TypeFloat, criutf.TypeDouble:
		return int64(v.F), true
	default:
		return v.Num, true
	}
}

// cell returns the (type, value) tuple python-style: value + has flag.
func cell(row map[string]*criutf.Val, key string) (int64, bool) {
	v := row[key]
	return valInt(v)
}

func be16(b []byte) int64 {
	if len(b) < 2 {
		return 0
	}
	return int64(binary.BigEndian.Uint16(b))
}

func be16s(b []byte) int64 {
	if len(b) < 2 {
		return 0
	}
	return int64(int16(binary.BigEndian.Uint16(b)))
}

func u8(b []byte) int64 {
	if len(b) < 1 {
		return 0
	}
	return int64(b[0])
}

// rowsField returns a table's rows under root field name.
func (a *ACB) rowsField(name string) []map[string]*criutf.Val {
	return tableRows(a.Table(name))
}

// Extract reproduces the reference cue->waveform-id mapping. Returns
// map[cueName][]sorted awb-segment ids.
func (a *ACB) Extract() map[string][]int64 {
	waveTable := a.rowsField("WaveformTable")
	synthTable := a.rowsField("SynthTable")
	seqTable := a.rowsField("SequenceTable")
	trackTable := a.rowsField("TrackTable")
	tevtTable := a.rowsField("TrackEventTable")

	var idsFromWaveform func(idx int64) map[int64]bool
	var collectFromSynth func(idx int64, depth int) map[int64]bool
	var collectFromSequence func(idx int64, depth int) map[int64]bool
	var collectFromTrack func(idx int64, depth int) map[int64]bool
	var collectFromBlockSequence func(idx int64, depth int) map[int64]bool

	add := func(s map[int64]bool, v int64) { s[v] = true }

	idsFromWaveform = func(idx int64) map[int64]bool {
		ids := map[int64]bool{}
		if idx < 0 || idx >= int64(len(waveTable)) {
			return ids
		}
		wf := waveTable[idx]
		streaming, hasS := cell(wf, "Streaming")
		if !hasS {
			_, hasAwb := wf["StreamAwbId"]
			if hasAwb {
				streaming = 1
			} else {
				streaming = 0
			}
		}
		if streaming == 1 || streaming == 2 {
			sid, ok := cell(wf, "StreamAwbId")
			if ok && sid != 0xFFFF {
				add(ids, sid)
			}
		}
		if streaming == 0 || streaming == 2 {
			mid, ok := cell(wf, "MemoryAwbId")
			if ok && mid != 0xFFFF {
				add(ids, mid)
			}
		}
		if _, ok := wf["Id"]; ok {
			wid, _ := cell(wf, "Id")
			if wid != 0xFFFF {
				add(ids, wid)
			}
		}
		return ids
	}

	collectFromSynth = func(idx int64, depth int) map[int64]bool {
		ids := map[int64]bool{}
		if idx < 0 || idx >= int64(len(synthTable)) || depth > 3 {
			return ids
		}
		ref := synthTable[idx]["ReferenceItems"]
		var data []byte
		if ref != nil && ref.IsBytes() {
			data = ref.B
		}
		for off := 0; off+4 <= len(data); off += 4 {
			itType := be16(data[off : off+2])
			itIdx := be16(data[off+2 : off+4])
			switch itType {
			case 0:
				return ids
			case 1:
				for k := range idsFromWaveform(itIdx) {
					add(ids, k)
				}
			case 2:
				for k := range collectFromSynth(itIdx, depth+1) {
					add(ids, k)
				}
			case 3:
				for k := range collectFromSequence(itIdx, depth+1) {
					add(ids, k)
				}
			default:
				return ids
			}
		}
		return ids
	}

	collectFromSequence = func(idx int64, depth int) map[int64]bool {
		ids := map[int64]bool{}
		if idx < 0 || idx >= int64(len(seqTable)) || depth > 3 {
			return ids
		}
		row := seqTable[idx]
		numTracks, _ := cell(row, "NumTracks")
		ti := row["TrackIndex"]
		var data []byte
		if ti != nil && ti.IsBytes() {
			data = ti.B
		}
		lim := numTracks
		if lim > int64(len(data)/2) {
			lim = int64(len(data) / 2)
		}
		for i := int64(0); i < lim; i++ {
			tIdx := be16s(data[i*2 : i*2+2])
			if tIdx >= 0 && tIdx < int64(len(trackTable)) {
				for k := range collectFromTrack(tIdx, depth+1) {
					add(ids, k)
				}
			}
		}
		return ids
	}

	collectFromTrack = func(idx int64, depth int) map[int64]bool {
		ids := map[int64]bool{}
		if idx < 0 || idx >= int64(len(trackTable)) {
			return ids
		}
		ev, ok := cell(trackTable[idx], "EventIndex")
		if !ok || ev == 0xFFFF {
			return ids
		}
		if ev < 0 || ev >= int64(len(tevtTable)) {
			return ids
		}
		cmd := tevtTable[ev]["Command"]
		var data []byte
		if cmd != nil && cmd.IsBytes() {
			data = cmd.B
		}
		pos := 0
		for pos+3 <= len(data) {
			code := be16(data[pos : pos+2])
			size := u8(data[pos+2 : pos+3])
			pos += 3
			if code == 2000 || code == 2003 {
				if pos+4 <= len(data) {
					tlvType := be16(data[pos : pos+2])
					tlvIndex := be16(data[pos+2 : pos+4])
					switch tlvType {
					case 2:
						for k := range collectFromSynth(tlvIndex, depth+1) {
							add(ids, k)
						}
					case 3:
						for k := range collectFromSequence(tlvIndex, depth+1) {
							add(ids, k)
						}
					}
				}
			}
			pos += int(size)
		}
		return ids
	}

	collectFromBlockSequence = func(idx int64, depth int) map[int64]bool {
		ids := map[int64]bool{}
		bq := a.rowsField("BlockSequenceTable")
		if bq == nil || idx < 0 || idx >= int64(len(bq)) {
			return ids
		}
		row := bq[idx]
		numTracks, _ := cell(row, "NumTracks")
		ti := row["TrackIndex"]
		var data []byte
		if ti != nil && ti.IsBytes() {
			data = ti.B
		}
		lim := numTracks
		if lim > int64(len(data)/2) {
			lim = int64(len(data) / 2)
		}
		for i := int64(0); i < lim; i++ {
			tIdx := be16s(data[i*2 : i*2+2])
			if tIdx >= 0 && tIdx < int64(len(trackTable)) {
				for k := range collectFromTrack(tIdx, depth+1) {
					add(ids, k)
				}
			}
		}
		return ids
	}

	cueNames := map[int64]string{}
	for _, item := range tableRows(a.Table("CueNameTable")) {
		if ci, ok := cell(item, "CueIndex"); ok {
			if cn := item["CueName"]; cn != nil && cn.IsString() {
				cueNames[ci] = cn.Str
			}
		}
	}

	cueTable := a.rowsField("CueTable")
	result := map[string][]int64{}
	for i, cueName := range cueNames {
		if i < 0 || i >= int64(len(cueTable)) {
			continue
		}
		refIndex, _ := cell(cueTable[i], "ReferenceIndex")
		refType, hasType := cell(cueTable[i], "ReferenceType")
		wave := map[int64]bool{}
		if !hasType {
			result[cueName] = nil
			continue
		}
		switch refType {
		case 1:
			for k := range idsFromWaveform(refIndex) {
				wave[k] = true
			}
		case 2:
			for k := range collectFromSynth(refIndex, 0) {
				wave[k] = true
			}
		case 3:
			for k := range collectFromSequence(refIndex, 0) {
				wave[k] = true
			}
		case 8:
			for k := range collectFromBlockSequence(refIndex, 0) {
				wave[k] = true
			}
		}
		// sorted
		var sorted []int64
		for k := range wave {
			sorted = append(sorted, k)
		}
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j] < sorted[i] {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		result[cueName] = sorted
	}
	return result
}
