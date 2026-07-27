package main

import (
	"os"
	"sync"
)

const maxLogSize = 20 * 1024 * 1024 // 20 MB

// rotatingWriter is an io.Writer that truncates the log file when it exceeds maxLogSize.
// No backup copy is retained.
type rotatingWriter struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

func newRotatingWriter(path string) (*rotatingWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingWriter{path: path, file: f, size: info.Size()}, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size+int64(len(p)) > maxLogSize {
		_ = w.file.Close()
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		w.file = f
		w.size = 0
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}
