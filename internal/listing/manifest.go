package listing

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Origins reads the manifest chisel wrote at path, a zstd jsonwall, for the
// slices each path comes from: path, with no trailing slash, to the slices
// chisel says it came from, as <package>_<slice>, sorted. A directory chisel
// made only to hold others is not in it.
func Origins(path string) (map[string][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	out := map[string][]string{}
	sc := bufio.NewScanner(dec)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var row struct {
			Kind   string   `json:"kind"`
			Path   string   `json:"path"`
			Slices []string `json:"slices"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if row.Kind == "path" {
			out[strings.TrimSuffix(row.Path, "/")] = row.Slices
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// Without drops from entries what a slice cut only to have chisel write its
// manifest at file added: the file, and each directory above it up to the
// root that no slice delivered and nothing else is under.
func Without(entries []Entry, file string, origins map[string][]string) []Entry {
	drop := map[string]bool{file: true}
	for dir := file[:strings.LastIndex(file, "/")]; dir != ""; dir = dir[:strings.LastIndex(dir, "/")] {
		if _, ok := origins[dir]; ok {
			break
		}
		alone := true
		for _, e := range entries {
			if strings.HasPrefix(e.Path, dir+"/") && !drop[e.Path] {
				alone = false
				break
			}
		}
		if !alone {
			break
		}
		drop[dir] = true
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if !drop[e.Path] {
			out = append(out, e)
		}
	}
	return out
}
