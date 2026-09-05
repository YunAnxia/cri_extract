// Package criawb parses CRI AFS2 containers (used as AWB files).
package criawb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// AWB is a parsed AFS2 container.
type AWB struct {
	Version byte
	Subkey  uint16
	Align   int
	IDs     []int64 // per-file ids as stored in the file table
	FileOfs []int64 // padded segment start offsets, len = len(IDs)+1 (last == end)
	Data    []byte
}

func typeByte(size int) (byte, error) {
	switch size {
	case 1:
		return 'B', nil
	case 2:
		return 'H', nil
	case 4:
		return 'I', nil
	case 8:
		return 'Q', nil
	}
	return 0, fmt.Errorf("awb: bad int size %d", size)
}

func unpack(typ byte, n int, b []byte) ([]int64, error) {
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		switch typ {
		case 'B':
			out[i] = int64(b[i])
		case 'H':
			out[i] = int64(binary.LittleEndian.Uint16(b[i*2:]))
		case 'I':
			out[i] = int64(binary.LittleEndian.Uint32(b[i*4:]))
		case 'Q':
			out[i] = int64(binary.LittleEndian.Uint64(b[i*8:]))
		default:
			return nil, errors.New("awb: bad type")
		}
	}
	return out, nil
}

// Parse reads an AFS2 container from data.
func Parse(data []byte) (*AWB, error) {
	if len(data) < 16 || string(data[:4]) != "AFS2" {
		return nil, errors.New("awb: not an AFS2 file")
	}
	le := binary.LittleEndian
	num := int(le.Uint32(data[8:12]))
	iis := int(le.Uint16(data[6:8]))
	ois := int(data[5])
	align := int(le.Uint16(data[12:14]))
	subkey := le.Uint16(data[14:16])
	if num <= 0 {
		return nil, errors.New("awb: no files")
	}
	ti, err := typeByte(iis)
	if err != nil {
		return nil, err
	}
	to, err := typeByte(ois)
	if err != nil {
		return nil, err
	}
	base := 16
	need := num*iis + (num+1)*ois
	if base+need > len(data) {
		return nil, errors.New("awb: header table truncated")
	}
	ids, err := unpack(ti, num, data[base:base+num*iis])
	if err != nil {
		return nil, err
	}
	rawOfs, err := unpack(to, num+1, data[base+num*iis:base+need])
	if err != nil {
		return nil, err
	}
	ofs := make([]int64, len(rawOfs))
	for i, o := range rawOfs {
		o64 := o
		if align > 0 {
			if m := o64 % int64(align); m != 0 {
				o64 += int64(align) - m
			}
		}
		ofs[i] = o64
	}
	// The stored end offset may already be EOF; alignment padding can push the
	// final entry just past it. Clamp only the last entry.
	if len(ofs) > 1 && ofs[len(ofs)-1] > int64(len(data)) {
		ofs[len(ofs)-1] = int64(len(data))
	}
	for i := 1; i < len(ofs); i++ {
		if ofs[i] <= ofs[i-1] {
			return nil, fmt.Errorf("awb: offsets not increasing (%d..%d)", ofs[i-1], ofs[i])
		}
	}
	if ofs[len(ofs)-1] > int64(len(data)) {
		return nil, errors.New("awb: last offset beyond EOF")
	}
	return &AWB{Version: data[4], Subkey: subkey, Align: align, IDs: ids, FileOfs: ofs, Data: data}, nil
}

// Segment returns the raw bytes of file i (0-based table entry).
func (a *AWB) Segment(i int) []byte {
	if i < 0 || i >= len(a.IDs) {
		return nil
	}
	return a.Data[a.FileOfs[i]:a.FileOfs[i+1]]
}

// OrdinalOfID returns the table index whose stored id equals id (first match).
func (a *AWB) OrdinalOfID(id int64) int {
	for i, v := range a.IDs {
		if v == id {
			return i
		}
	}
	return -1
}
