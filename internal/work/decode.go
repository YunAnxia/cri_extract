package work

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type decodeResult struct {
	ei, si int
	err    error
}

// decodeAll runs vgmstream on the intermediate hca files (or just keeps the
// hca when no wav output is requested) with a bounded worker pool.
func decodeAll(o Options, jobs []*job) []decodeResult {
	res := make([]decodeResult, len(jobs))
	var wg sync.WaitGroup
	ch := make(chan int)
	for w := 0; w < o.Threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range ch {
				j := jobs[idx]
				res[idx] = decodeResult{ei: j.ei, si: j.si, err: runJob(o, j)}
			}
		}()
	}
	for i := range jobs {
		ch <- i
	}
	close(ch)
	wg.Wait()
	return res
}

func runJob(o Options, j *job) error {
	// keep hca if requested
	if j.hcaOut != "" {
		if err := os.MkdirAll(filepath.Dir(j.hcaOut), 0o755); err != nil {
			return err
		}
		if err := copyFile(j.cacheHCA, j.hcaOut); err != nil {
			return err
		}
	}
	if j.wavOut == "" {
		return nil // hca-only
	}
	if st, err := os.Stat(j.wavOut); err == nil && st.Size() > 0 {
		return nil // resume
	}
	if err := os.MkdirAll(filepath.Dir(j.wavOut), 0o755); err != nil {
		return err
	}
	args := []string{"-o", j.wavOut}
	if o.NoLoop {
		args = append(args, "-i")
	}
	args = append(args, j.cacheHCA)
	cmd := exec.Command(o.Vgmstream, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vgmstream: %v\n%s", err, truncate(string(out), 400))
	}
	if st, err := os.Stat(j.wavOut); err != nil || st.Size() == 0 {
		return fmt.Errorf("vgmstream produced no output")
	}
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
