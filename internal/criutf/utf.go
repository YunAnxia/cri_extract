// Package criutf parses CRIWARE @UTF tables (and encrypted EUTF via xor),
// mirroring the behavior of the reference Python implementation used to
// validate this dataset.
package criutf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

// UTF type tags.
const (
	TypeUChar  = 0
	TypeChar   = 1
	TypeUShort = 2
	TypeShort  = 3
	TypeUInt   = 4
	TypeInt    = 5
	TypeULLong = 6
	TypeLLong  = 7
	TypeFloat  = 8
	TypeDouble = 9
	TypeString = 10
	TypeBytes  = 11
)

// Val is one typed cell of a row. For numeric types Num holds the value
// (float values also set F). Strings live in Str, bytes in B.
type Val struct {
	Type int
	Num  int64
	F    float64
	Str  string
	B    []byte
}

func (v *Val) IsBytes() bool  { return v != nil && v.Type == TypeBytes }
func (v *Val) IsString() bool { return v != nil && v.Type == TypeString }
func (v *Val) Int() int64 {
	if v == nil {
		return 0
	}
	return v.Num
}

// Table is a parsed @UTF: column names come from the table itself.
type Table struct {
	Name     string
	Encoding string
	Rows     []map[string]*Val
	NumRows  int
	NumCols  int
}

// cell sets the field on row with python semantics (append-on-duplicate is
// collapsed to first-wins; duplicates are extremely rare in practice).
func setField(row map[string]*Val, name string, v *Val) {
	if _, ok := row[name]; !ok {
		row[name] = v
	}
}

type cursor struct {
	data []byte
	pos  int
}

func (c *cursor) read(n int) ([]byte, error) {
	if c.pos+n > len(c.data) {
		return nil, errors.New("utf: out of data")
	}
	b := c.data[c.pos : c.pos+n]
	c.pos += n
	return b, nil
}

func (c *cursor) u8() (byte, error) {
	b, err := c.read(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// utfHeader mirrors Struct(">4sIIIIIHHI").
type utfHeader struct {
	Magic        [4]byte
	TableSize    uint32
	RowsOffset   uint32
	StringOffset uint32
	DataOffset   uint32
	TableName    uint32
	NumColumns   uint16
	RowLength    uint16
	NumRows      uint32
}

func parseHeader(d []byte) (utfHeader, error) {
	var h utfHeader
	if len(d) < 32 {
		return h, errors.New("utf: header too short")
	}
	copy(h.Magic[:], d[0:4])
	be := binary.BigEndian
	h.TableSize = be.Uint32(d[4:8])
	h.RowsOffset = be.Uint32(d[8:12])
	h.StringOffset = be.Uint32(d[12:16])
	h.DataOffset = be.Uint32(d[16:20])
	h.TableName = be.Uint32(d[20:24])
	h.NumColumns = be.Uint16(d[24:26])
	h.RowLength = be.Uint16(d[26:28])
	h.NumRows = be.Uint32(d[28:32])
	return h, nil
}

func decryptEUTF(d []byte) []byte {
	out := make([]byte, len(d))
	copy(out, d)
	m := uint64(0x655F)
	t := uint64(0x4115)
	for i := range out {
		out[i] ^= byte(0xFF & m)
		m = (m * t) & 0xFFFFFFFF
	}
	return out
}

// typeSize returns byte size of numeric struct types.
func typeSize(t int) int {
	switch t {
	case TypeUChar, TypeChar:
		return 1
	case TypeUShort, TypeShort:
		return 2
	case TypeUInt, TypeInt, TypeFloat:
		return 4
	case TypeULLong, TypeLLong, TypeDouble:
		return 8
	}
	return 0
}

func signedType(t int) bool {
	return t == TypeChar || t == TypeShort || t == TypeInt || t == TypeLLong
}

// unpackNum decodes one numeric value (types 0..9) big-endian.
func unpackNum(t int, b []byte) (int64, float64, error) {
	be := binary.BigEndian
	switch t {
	case TypeUChar:
		return int64(b[0]), 0, nil
	case TypeChar:
		return int64(int8(b[0])), 0, nil
	case TypeUShort:
		return int64(be.Uint16(b)), 0, nil
	case TypeShort:
		return int64(int16(be.Uint16(b))), 0, nil
	case TypeUInt:
		return int64(be.Uint32(b)), 0, nil
	case TypeInt:
		return int64(int32(be.Uint32(b))), 0, nil
	case TypeULLong:
		return int64(be.Uint64(b)), 0, nil
	case TypeLLong:
		return int64(be.Uint64(b)), 0, nil
	case TypeFloat:
		return 0, float64(math.Float32frombits(be.Uint32(b))), nil
	case TypeDouble:
		return 0, math.Float64frombits(be.Uint64(b)), nil
	}
	return 0, 0, fmt.Errorf("utf: not numeric type %d", t)
}

type storageFlag int

const (
	flagData     storageFlag = 0x5
	flagTuple    storageFlag = 0x3
	flagConstant storageFlag = 0x1
)

type colDef struct {
	flag storageFlag
	typ  int
	// data columns: name position in strings
	pos uint32
	// tuple: name pos + inline value bytes
	tupleVal []byte
	// constant: none (value from later rows? constants carry no data)
}

// finder maps a byte offset into the concatenated string region (including
// terminators) to the index of the string that contains it, mirroring the
// reference implementation.
func finder(offset uint32, strs [][]byte) (int, error) {
	sum := uint32(0)
	for i, s := range strs {
		if sum < offset {
			sum += uint32(len(s)) + 1
			continue
		}
		return i, nil
	}
	return 0, errors.New("utf: string lookup failed")
}

func decodeString(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	// Reference parser falls back to shift-jis then utf-16; for the names we
	// care about everything is ASCII/UTF-8. Keep a lossless passthrough.
	return string(raw)
}

// Parse decodes a @UTF (or EUTF) table from data.
func Parse(data []byte) (*Table, error) {
	h, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(h.Magic[:], []byte("@UTF")) {
		if bytes.Equal(h.Magic[:], []byte{0x1f, 0x9e, 0xf3, 0xf5}) {
			data = decryptEUTF(data)
			h, err = parseHeader(data)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(h.Magic[:], []byte("@UTF")) {
				return nil, errors.New("utf: EUTF decryption failed")
			}
		} else {
			return nil, fmt.Errorf("utf: not an @UTF chunk (%x)", h.Magic)
		}
	}
	if h.DataOffset < 0x18 {
		return nil, errors.New("utf: bad data offset")
	}
	// Reference reads (dataOffset - 0x18) bytes starting right after the
	// 32-byte header, i.e. region [32, dataOffset+8).
	region := data[32:]
	cut := int(h.DataOffset) - 0x18
	if cut > len(region) {
		cut = len(region)
	}
	c := &cursor{data: region[:cut]}

	colTypes := [4][]colDef{} // data/tuple/constant index 0,1,2 (3 unused)
	for i := 0; i < int(h.NumColumns); i++ {
		flagB, err := c.u8()
		if err != nil {
			return nil, err
		}
		stflag := flagB >> 4
		typeflag := flagB & 0xF
		cd := colDef{typ: int(typeflag)}
		switch stflag {
		case byte(flagData):
			v4, err := c.read(4)
			if err != nil {
				return nil, err
			}
			cd.flag = flagData
			cd.pos = binary.BigEndian.Uint32(v4)
			colTypes[0] = append(colTypes[0], cd)
		case byte(flagTuple):
			v4, err := c.read(4)
			if err != nil {
				return nil, err
			}
			sz := 4
			switch int(typeflag) {
			case TypeString:
				sz = 4 // a string index
			case TypeBytes:
				sz = 8 // (offset,length)
			default:
				sz = typeSize(int(typeflag))
			}
			raw, err := c.read(sz)
			if err != nil {
				return nil, err
			}
			cd.flag = flagTuple
			cd.pos = binary.BigEndian.Uint32(v4)
			cd.tupleVal = raw
			colTypes[1] = append(colTypes[1], cd)
		case byte(flagConstant):
			v4, err := c.read(4)
			if err != nil {
				return nil, err
			}
			cd.flag = flagConstant
			cd.pos = binary.BigEndian.Uint32(v4)
			colTypes[2] = append(colTypes[2], cd)
		default:
			return nil, fmt.Errorf("utf: unknown storage flag 0x%x", stflag)
		}
	}

	// numeric row data: rows*numDataColumns values, sequential.
	dataCols := colTypes[0]
	rowsRaw := make([][]int64, 0)
	rowF := make([][]float64, 0)
	for j := 0; j < int(h.NumRows); j++ {
		vals := make([]int64, 0, len(dataCols))
		floats := make([]float64, 0, len(dataCols))
		for _, cd := range dataCols {
			var raw []byte
			var err error
			switch cd.typ {
			case TypeString:
				raw, err = c.read(4) // string index
			case TypeBytes:
				raw, err = c.read(8) // (offset,length)
			default:
				raw, err = c.read(typeSize(cd.typ))
			}
			if err != nil {
				return nil, err
			}
			n, f, err := unpackNum(cd.typ, raw)
			if err != nil {
				// string/bytes: pack raw value into n
				switch cd.typ {
				case TypeString:
					vals = append(vals, int64(binary.BigEndian.Uint32(raw)))
					floats = append(floats, 0)
				case TypeBytes:
					vals = append(vals, int64(binary.BigEndian.Uint64(raw)))
					floats = append(floats, 0)
				default:
					return nil, err
				}
				continue
			}
			if cd.typ == TypeFloat || cd.typ == TypeDouble {
				floats = append(floats, f)
				vals = append(vals, 0)
			} else {
				vals = append(vals, n)
				floats = append(floats, 0)
			}
		}
		rowsRaw = append(rowsRaw, vals)
		rowF = append(rowF, floats)
	}

	// The remainder of the region is the string table (may include a final
	// empty segment from the trailing NUL).
	rest := c.data[c.pos:]
	rawStrings := bytes.Split(rest, []byte{0})
	strings := make([][]byte, 0, len(rawStrings))
	for _, s := range rawStrings {
		strings = append(strings, s)
	}
	strVals := make([]string, len(strings))
	for i, s := range strings {
		strVals[i] = decodeString(s)
	}

	// Column names / constant scaffolding: build constant cells first (they
	// apply to every row) and name maps for data/tuple columns.
	colName := func(cd colDef) (string, error) {
		idx, err := finder(cd.pos, strings)
		if err != nil {
			return "", err
		}
		return strVals[idx], nil
	}

	// constants: name -> Val (value nil for numerics, placeholder for string)
	type constCell struct {
		name string
		v    *Val
	}
	constCells := make([]constCell, 0, len(colTypes[2]))
	for _, cd := range colTypes[2] {
		name, err := colName(cd)
		if err != nil {
			return nil, err
		}
		if cd.typ == TypeString {
			constCells = append(constCells, constCell{name, &Val{Type: TypeString, Str: "<NULL>"}})
		} else if cd.typ == TypeBytes {
			constCells = append(constCells, constCell{name, &Val{Type: TypeBytes}})
		} else {
			constCells = append(constCells, constCell{name, &Val{Type: cd.typ}})
		}
	}

	// helper to resolve a data/tuple cell: tuple cells carry inline value.
	rows := make([]map[string]*Val, 0, int(h.NumRows))
	cellFor := func(cd colDef, num int64, f float64, isTuple bool) (*Val, error) {
		switch cd.typ {
		case TypeString:
			var idx int64
			if isTuple {
				be := binary.BigEndian
				idx = int64(be.Uint32(cd.tupleVal[:4]))
			} else {
				idx = num & 0xFFFFFFFF
			}
			si, err := finder(uint32(idx), strings)
			if err != nil {
				return nil, err
			}
			return &Val{Type: TypeString, Str: strVals[si]}, nil
		case TypeBytes:
			var off, ln uint32
			if isTuple {
				off = binary.BigEndian.Uint32(cd.tupleVal[:4])
				ln = binary.BigEndian.Uint32(cd.tupleVal[4:8])
			} else {
				off = uint32(num >> 32)
				ln = uint32(num & 0xFFFFFFFF)
			}
			start := int(h.DataOffset) + int(off) + 8
			if start < 0 || start+int(ln) > len(data) {
				return nil, fmt.Errorf("utf: bytes range out of table (%d..%d)", start, start+int(ln))
			}
			b := make([]byte, ln)
			copy(b, data[start:start+int(ln)])
			return &Val{Type: TypeBytes, B: b}, nil
		case TypeFloat, TypeDouble:
			if isTuple {
				if len(cd.tupleVal) >= 4 {
					if cd.typ == TypeFloat {
						return &Val{Type: cd.typ, F: float64(math.Float32frombits(binary.BigEndian.Uint32(cd.tupleVal[:4])))}, nil
					}
					return &Val{Type: cd.typ, F: math.Float64frombits(binary.BigEndian.Uint64(cd.tupleVal[:8]))}, nil
				}
				return &Val{Type: cd.typ, F: f}, nil
			}
			return &Val{Type: cd.typ, F: f}, nil
		default:
			if isTuple && len(cd.tupleVal) > 0 {
				n, ff, err := unpackNum(cd.typ, cd.tupleVal)
				if err != nil {
					return nil, err
				}
				if cd.typ == TypeFloat || cd.typ == TypeDouble {
					return &Val{Type: cd.typ, F: ff}, nil
				}
				return &Val{Type: cd.typ, Num: n}, nil
			}
			return &Val{Type: cd.typ, Num: num}, nil
		}
	}

	// data-column name lookup per column index (by definition order)
	dataNames := make([]string, len(dataCols))
	for i, cd := range dataCols {
		n, err := colName(cd)
		if err != nil {
			return nil, err
		}
		dataNames[i] = n
	}
	tupleCells := make([]struct {
		name string
		cd   colDef
	}, 0, len(colTypes[1]))
	for _, cd := range colTypes[1] {
		name, err := colName(cd)
		if err != nil {
			return nil, err
		}
		tupleCells = append(tupleCells, struct {
			name string
			cd   colDef
		}{name, cd})
	}

	if len(dataCols) == 0 {
		// constant-only table -> single pseudo row
		row := map[string]*Val{}
		for _, cc := range constCells {
			setField(row, cc.name, cc.v)
		}
		rows = append(rows, row)
	}
	for j := 0; j < len(rowsRaw); j++ {
		row := map[string]*Val{}
		for i, name := range dataNames {
			v, err := cellFor(dataCols[i], rowsRaw[j][i], rowF[j][i], false)
			if err != nil {
				return nil, err
			}
			setField(row, name, v)
		}
		// tuple columns belong to every row (first-wins on duplicates)
		for _, tc := range tupleCells {
			v, err := cellFor(tc.cd, 0, 0, true)
			if err != nil {
				return nil, err
			}
			setField(row, tc.name, v)
		}
		for _, cc := range constCells {
			setField(row, cc.name, cc.v)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, map[string]*Val{})
	}

	// table name
	tname := ""
	if int(h.TableName) < len(strVals) {
		tname = strVals[h.TableName]
	}
	return &Table{Name: tname, Rows: rows, NumRows: len(rows), NumCols: int(h.NumColumns)}, nil
}
