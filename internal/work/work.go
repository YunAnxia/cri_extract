// Package work orchestrates ACB/AWB pairing, naming, extraction and decode.
package work

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cri_extract/internal/criacb"
	"cri_extract/internal/criawb"
	"cri_extract/internal/criutf"
)

var invalidChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

// Clean sanitizes a name for Windows filenames.
func Clean(name string) string {
	return invalidChars.ReplaceAllString(name, "_")
}

// Options configures a run.
type Options struct {
	InDir     string
	OutDir    string
	Vgmstream string
	Format    string // wav | hca | both | meta (default wav)
	Threads   int
	LimitACB  int // 0 = all
	NoLoop    bool
	FirstOnly bool // export only the first track of multi-track ACBs
	Verbose   bool
}

type WaveMeta struct {
	EncodeType   *int64 `json:",omitempty"`
	Streaming    *int64 `json:",omitempty"`
	NumChannels  *int64 `json:",omitempty"`
	SamplingRate *int64 `json:",omitempty"`
	NumSamples   *int64 `json:",omitempty"`
	LoopFlag     *int64 `json:",omitempty"`
	MemoryAwbId  *int64 `json:",omitempty"`
}

type Entry struct {
	ACB        string    `json:"acb_sha1,omitempty"`
	ACBName    string    `json:"acb_name"`
	AWB        string    `json:"awb_sha1,omitempty"`
	AWBMissing bool      `json:"awb_missing,omitempty"`
	Cues       []string  `json:"cues,omitempty"`
	Errors     []string  `json:"errors,omitempty"`
	Segments   []*SegOut `json:"segments,omitempty"`
}

type SegOut struct {
	Seg    int      `json:"seg"`
	ID     int64    `json:"awb_id"`
	Names  []string `json:"names"`
	HCAOut string   `json:"hca,omitempty"`
	WAVOut string   `json:"wav,omitempty"`
	Size   int64    `json:"size"`
	WaveMeta
	Status string `json:"status"`
}

type Manifest struct {
	Input   string  `json:"input"`
	Format  string  `json:"format"`
	Entries []Entry `json:"entries"`
}

type job struct {
	cacheHCA string
	wavOut   string
	hcaOut   string // final hca path when kept
	ei, si   int
	noLoop   bool
}

func md5Index(dir string) (map[[16]byte]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	idx := map[[16]byte]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".awb") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		h := md5.New()
		buf := make([]byte, 1<<20)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				h.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		f.Close()
		var sum [16]byte
		copy(sum[:], h.Sum(nil))
		idx[sum] = e.Name()
	}
	return idx, nil
}

// Run executes the pipeline and writes out/manifest.json.
func Run(o Options) (*Manifest, error) {
	if o.Threads <= 0 {
		o.Threads = 8
	}
	if o.Format == "" {
		o.Format = "wav"
	}
	valid := map[string]bool{"wav": true, "hca": true, "both": true, "meta": true}
	if !valid[o.Format] {
		return nil, fmt.Errorf("bad format %q", o.Format)
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return nil, err
	}

	acbFiles, _ := filepath.Glob(filepath.Join(o.InDir, "*.acb"))
	sort.Strings(acbFiles)
	if o.LimitACB > 0 && len(acbFiles) > o.LimitACB {
		acbFiles = acbFiles[:o.LimitACB]
	}

	acbBytes := map[string][]byte{}
	needIndex := false
	for _, p := range acbFiles {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		acbBytes[p] = b
		if a, err := criacb.Open(b); err == nil && len(a.StreamHash()) == 16 {
			needIndex = true
		}
	}

	awbByName := map[string][]byte{}
	list, _ := os.ReadDir(o.InDir)
	for _, e := range list {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".awb") {
			if b, err := os.ReadFile(filepath.Join(o.InDir, e.Name())); err == nil {
				awbByName[e.Name()] = b
			}
		}
	}
	var awbIdx map[[16]byte]string
	if needIndex {
		idx, idxErr := md5Index(o.InDir)
		if idxErr != nil {
			return nil, idxErr
		}
		awbIdx = idx
	}

	cacheDir := filepath.Join(o.OutDir, ".hca_cache")
	decoding := o.Format != "meta"
	if decoding {
		_ = os.MkdirAll(cacheDir, 0o755)
	}

	seen := map[string]int{}
	var entries []Entry
	var jobs []*job
	order := 0

	for _, acbPath := range acbFiles {
		ent := Entry{ACB: strings.TrimSuffix(filepath.Base(acbPath), ".acb")}
		b, ok := acbBytes[acbPath]
		if !ok {
			ent.Errors = append(ent.Errors, "read failed")
			entries = append(entries, ent)
			continue
		}
		acb, err := criacb.Open(b)
		if err != nil {
			ent.Errors = append(ent.Errors, err.Error())
			entries = append(entries, ent)
			continue
		}
		ent.ACBName = acb.Name()
		ent.Cues = acb.CueNames()

		var awbData []byte
		awbFile := ""
		switch {
		case len(acb.EmbeddedAWB()) > 0:
			awbData = acb.EmbeddedAWB()
		default:
			same := filepath.Join(o.InDir, strings.TrimSuffix(filepath.Base(acbPath), ".acb")+".awb")
			if b, err := os.ReadFile(same); err == nil {
				awbData, awbFile = b, filepath.Base(same)
			} else if sh := acb.StreamHash(); sh != nil && awbIdx != nil {
				var k [16]byte
				copy(k[:], sh)
				if n, ok := awbIdx[k]; ok {
					if b, ok := awbByName[n]; ok {
						awbData, awbFile = b, n
					}
				}
			}
		}
		if len(awbData) == 0 {
			ent.AWBMissing = true
			entries = append(entries, ent)
			continue
		}
		ent.AWB = strings.TrimSuffix(awbFile, ".awb")

		awb, err := criawb.Parse(awbData)
		if err != nil {
			ent.Errors = append(ent.Errors, err.Error())
			entries = append(entries, ent)
			continue
		}

		metaByID := map[int64]WaveMeta{}
		if wt := acb.Table("WaveformTable"); wt != nil {
			for _, r := range wt.Rows {
				sid, ok := numVal(r["StreamAwbId"])
				if !ok {
					continue
				}
				m := WaveMeta{}
				for key, dst := range map[string]**int64{
					"EncodeType": &m.EncodeType, "Streaming": &m.Streaming,
					"NumChannels": &m.NumChannels, "SamplingRate": &m.SamplingRate,
					"NumSamples": &m.NumSamples, "LoopFlag": &m.LoopFlag,
					"MemoryAwbId": &m.MemoryAwbId,
				} {
					if v, ok := numVal(r[key]); ok {
						x := v
						*dst = &x
					}
				}
				metaByID[sid] = m
			}
		}

		cueMap := acb.Extract()
		// iterate cues in CueNameTable order (deterministic, matches the
		// reference implementation's dict insertion order)
		cueOrder := acb.CueNames()
		extra := make([]string, 0)
		for cue := range cueMap {
			seenCue := false
			for _, c := range cueOrder {
				if c == cue {
					seenCue = true
					break
				}
			}
			if !seenCue {
				extra = append(extra, cue)
			}
		}
		sort.Strings(extra)
		cueOrder = append(cueOrder, extra...)
		namesByID := map[int64][]string{}
		for _, cue := range cueOrder {
			ids := cueMap[cue]
			if len(ids) == 0 {
				continue
			}
			if len(ids) == 1 {
				namesByID[ids[0]] = append(namesByID[ids[0]], cue)
			} else {
				for n, idx := range ids {
					namesByID[idx] = append(namesByID[idx], fmt.Sprintf("%s_#%d", cue, n+1))
				}
			}
		}

		base := Clean(ent.ACBName)
		for oi, id := range awb.IDs {
			if o.FirstOnly && oi > 0 {
				so := &SegOut{Seg: oi, ID: id, Names: nil, Status: "skipped (first-track only)"}
				ent.Segments = append(ent.Segments, so)
				continue
			}
			seg := awb.Segment(oi)
			if len(seg) == 0 {
				continue
			}
			names := namesByID[id]
			var stem string
			switch {
			case len(names) == 0:
				stem = fmt.Sprintf("%s__seg%d", base, oi)
			case len(names) == 1:
				stem = Clean(names[0])
			default:
				parts := make([]string, len(names))
				for i, nm := range names {
					parts[i] = Clean(nm)
				}
				stem = strings.Join(parts, "&")
			}
			hcaName := stem + ".hca"
			seen[hcaName]++
			if c := seen[hcaName]; c > 1 {
				hcaName = fmt.Sprintf("%s__%d.hca", stem, c)
			}
			wavName := strings.TrimSuffix(hcaName, ".hca") + ".wav"

			so := &SegOut{Seg: oi, ID: id, Names: names, Size: int64(len(seg)), Status: "ok", HCAOut: hcaName}
			if m, ok := metaByID[id]; ok {
				so.WaveMeta = m
			}
			if o.Format != "meta" {
				cachePath := filepath.Join(cacheDir, fmt.Sprintf("%06d_%s", order, hcaName))
				if err := os.WriteFile(cachePath, seg, 0o644); err != nil {
					so.Status = err.Error()
					ent.Errors = append(ent.Errors, so.Status)
					ent.Segments = append(ent.Segments, so)
					continue
				}
				hcaFinal := ""
				if o.Format == "hca" || o.Format == "both" {
					hcaFinal = filepath.Join(o.OutDir, hcaName)
				}
				wavFinal := ""
				if o.Format != "hca" {
					wavFinal = filepath.Join(o.OutDir, wavName)
					so.WAVOut = wavName
				}
				order++
				jobs = append(jobs, &job{
					cacheHCA: cachePath, wavOut: wavFinal, hcaOut: hcaFinal,
					ei: len(entries), si: len(ent.Segments), noLoop: o.NoLoop,
				})
			}
			ent.Segments = append(ent.Segments, so)
		}
		entries = append(entries, ent)
	}

	if decoding && o.Vgmstream == "" {
		return nil, fmt.Errorf("vgmstream-cli path required for format %q", o.Format)
	}
	if decoding {
		errs := decodeAll(o, jobs)
		for _, e := range errs {
			if e.err != nil {
				entries[e.ei].Segments[e.si].Status = "decode: " + e.err.Error()
				entries[e.ei].Errors = append(entries[e.ei].Errors, e.err.Error())
			}
		}
		_ = os.RemoveAll(cacheDir)
	}

	m := &Manifest{Input: o.InDir, Format: o.Format, Entries: entries}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(o.OutDir, "manifest.json"), data, 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

func numVal(v *criutf.Val) (int64, bool) {
	if v == nil {
		return 0, false
	}
	return v.Num, true
}
