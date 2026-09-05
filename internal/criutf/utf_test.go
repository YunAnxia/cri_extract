package criutf

import (
	"bytes"
	"os"
	"testing"
)

const matrixAcb = `D:\work\PyCriCodecs\matrix\00bf6cb2f74e141c22b7efa4e5e547e871170dc6.acb`

func load(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(matrixAcb)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	return b
}

func TestParseRoot(t *testing.T) {
	root, err := Parse(load(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if root.NumRows != 1 {
		t.Fatalf("want 1 root row, got %d", root.NumRows)
	}
	row := root.Rows[0]
	// every cell is a *Val
	name, ok := row["Name"]
	if !ok {
		t.Fatalf("no Name field; keys sample: %v", keys(row, 12))
	}
	if name.Type != TypeString || name.Str != "m_ev18_song_1" {
		t.Fatalf("Name = %#v", name)
	}
	ver := row["Version"]
	if ver == nil || ver.Type != TypeUInt {
		t.Fatalf("Version bad: %#v", ver)
	}
	cn, ok := row["CueNameTable"]
	if !ok || !cn.IsBytes() {
		t.Fatalf("CueNameTable not bytes: %#v", cn)
	}
	if !bytes.HasPrefix(cn.B, []byte("@UTF")) {
		t.Fatalf("CueNameTable not nested @UTF: %x", cn.B[:8])
	}
	sub, err := Parse(cn.B)
	if err != nil {
		t.Fatalf("nested parse: %v", err)
	}
	if sub.Rows[0]["CueName"].Str != "m_ev18_song_1" {
		t.Fatalf("cue name: %#v", sub.Rows[0]["CueName"])
	}
}

func TestParseWaveform(t *testing.T) {
	root, err := Parse(load(t))
	if err != nil {
		t.Fatal(err)
	}
	wf := root.Rows[0]["WaveformTable"].B
	sub, err := Parse(wf)
	if err != nil {
		t.Fatalf("waveform: %v", err)
	}
	r := sub.Rows[0]
	if got := r["EncodeType"].Int(); got != 2 {
		t.Fatalf("EncodeType = %d", got)
	}
	if got := r["SamplingRate"].Int(); got != 48000 {
		t.Fatalf("SamplingRate = %d", got)
	}
	if got := r["NumChannels"].Int(); got != 2 {
		t.Fatalf("NumChannels = %d", got)
	}
	if got := r["StreamAwbId"].Int(); got != 0 {
		t.Fatalf("StreamAwbId = %d", got)
	}
}

func keys(row map[string]*Val, n int) []string {
	var out []string
	for k := range row {
		out = append(out, k)
		if len(out) >= n {
			break
		}
	}
	return out
}
