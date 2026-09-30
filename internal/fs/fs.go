// Package fs is the small set of file helpers the entrypoints share.
package fs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func IsDir(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func NonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// The rename means a reader never sees a half-written file.
func WriteAtomic(path, content string) error {
	// The temp name carries the pid so two panels writing the same file cannot
	// fill one temp between them, and the target keeps the mode it had.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

const (
	tailChunk = 8192
	tailLimit = 1 << 20
)

// TailLines returns the last n lines of a file. It reads backwards from the end
// until it has them, because the log is re-read on every draw while a command
// keeps appending to it.
func TailLines(path string, n int) ([]string, error) {
	if n < 1 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var buf []byte
	found := 0
	for start := info.Size(); start > 0; {
		step := int64(tailChunk)
		if step > start {
			step = start
		}
		start -= step
		block := make([]byte, step)
		read, err := f.ReadAt(block, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		// A short read means the file shrank under us; the rest of the block is
		// zeroes that must not reach the screen.
		block = block[:read]
		buf = append(block, buf...)
		found += bytes.Count(block, []byte{'\n'})
		if start == 0 || found > n || len(buf) >= tailLimit {
			break
		}
	}
	if len(buf) == 0 {
		return nil, nil
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func TailBytes(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	start := int64(0)
	if size > max {
		start = size - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
