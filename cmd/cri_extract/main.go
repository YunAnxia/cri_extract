// cri_extract: extract audio from CRI ACB/AWB containers with original cue
// names & metadata, decoding through vgmstream-cli.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cri_extract/internal/work"
)

func main() {
	var (
		inDir     = flag.String("in", "", "directory containing .acb/.awb files (required)")
		outDir    = flag.String("out", "", "output directory (default: <in>_out)")
		vgm       = flag.String("vgmstream", "", "path to vgmstream-cli[.exe] (auto-detected)")
		format    = flag.String("format", "wav", "output format: wav | hca | both | meta")
		threads   = flag.Int("threads", runtime.NumCPU(), "parallel decode workers")
		limit     = flag.Int("limit", 0, "only process the first N acb files (0 = all)")
		firstOnly = flag.Bool("first-track", false, "export only the first track of multi-track ACBs")
		loops     = flag.Bool("loops", false, "honor loop info when decoding (default: single full pass)")
		verbose   = flag.Bool("verbose", false, "verbose logging")
	)
	flag.Parse()

	if *inDir == "" {
		fmt.Fprintln(os.Stderr, "error: -in is required")
		flag.Usage()
		os.Exit(2)
	}
	if *outDir == "" {
		*outDir = strings.TrimSuffix(*inDir, string(os.PathSeparator)) + "_out"
	}
	f := strings.ToLower(*format)
	switch f {
	case "wav", "hca", "both", "meta":
	default:
		fmt.Fprintf(os.Stderr, "error: bad -format %q\n", f)
		os.Exit(2)
	}

	vgmPath := *vgm
	if vgmPath == "" && f != "meta" {
		p, err := findVgmstream()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		vgmPath = p
	}

	start := time.Now()
	o := work.Options{
		InDir:     *inDir,
		OutDir:    *outDir,
		Vgmstream: vgmPath,
		Format:    f,
		Threads:   *threads,
		LimitACB:  *limit,
		NoLoop:    !*loops,
		FirstOnly: *firstOnly,
		Verbose:   *verbose,
	}
	m, err := work.Run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	nAcb, nSeg, nErr := 0, 0, 0
	for _, e := range m.Entries {
		if len(e.Errors) > 0 {
			nErr += len(e.Errors)
		}
		if len(e.Segments) > 0 {
			nAcb++
			nSeg += len(e.Segments)
		}
	}
	fmt.Printf("done: acb=%d segments=%d errors=%d format=%s elapsed=%s\n",
		nAcb, nSeg, nErr, m.Format, time.Since(start).Round(time.Millisecond))
	if vgmPath != "" {
		fmt.Println("vgmstream:", vgmPath)
	}
	fmt.Println("manifest:", filepath.Join(*outDir, "manifest.json"))
}

func findVgmstream() (string, error) {
	cands := []string{"vgmstream-cli", "vgmstream-cli.exe"}
	for _, c := range cands {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	fixed := []string{
		`D:\vgmstream-win64\vgmstream-cli.exe`,
		`C:\vgmstream-win64\vgmstream-cli.exe`,
	}
	for _, p := range fixed {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("vgmstream-cli not found; pass -vgmstream <path>")
}
