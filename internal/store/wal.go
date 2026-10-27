// Package store persists accepted transactions as an append-only JSON-lines
// log so the in-memory engine can be rebuilt after a restart.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// Log is an append-only transaction log. A nil *Log is a valid no-op log.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	w    *bufio.Writer
	sync bool
}

// Open opens (or creates) the log in dir. If fsync is true every append is
// flushed to disk before returning.
func Open(dir string, fsync bool) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "transactions.jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Log{path: p, f: f, w: bufio.NewWriterSize(f, 1<<16), sync: fsync}, nil
}

// Replay streams every logged transaction to fn in batches.
func (l *Log) Replay(fn func([]model.Transaction)) (int, error) {
	if l == nil {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(l.f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	batch := make([]model.Transaction, 0, 4096)
	n, line := 0, 0
	for sc.Scan() {
		line++
		var t model.Transaction
		if err := json.Unmarshal(sc.Bytes(), &t); err != nil {
			// A torn last line after a crash is skipped; anything else is reported.
			fmt.Fprintf(os.Stderr, "store: skipping line %d: %v\n", line, err)
			continue
		}
		batch = append(batch, t)
		if len(batch) == cap(batch) {
			fn(batch)
			n += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		fn(batch)
		n += len(batch)
	}
	_, err := l.f.Seek(0, io.SeekEnd)
	if err == nil {
		err = sc.Err()
	}
	return n, err
}

// Append writes transactions to the log.
func (l *Log) Append(txns []model.Transaction) error {
	if l == nil || len(txns) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	enc := json.NewEncoder(l.w)
	for i := range txns {
		if err := enc.Encode(&txns[i]); err != nil {
			return err
		}
	}
	if err := l.w.Flush(); err != nil {
		return err
	}
	if l.sync {
		return l.f.Sync()
	}
	return nil
}

// Rewrite atomically replaces the log contents (used after compaction or reset).
func (l *Log) Rewrite(txns []model.Transaction) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	tmp := l.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<16)
	enc := json.NewEncoder(w)
	for i := range txns {
		if err := enc.Encode(&txns[i]); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	l.f.Close()
	nf, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.f = nf
	l.w = bufio.NewWriterSize(nf, 1<<16)
	return nil
}

// Close flushes and closes the log.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w.Flush()
	return l.f.Close()
}
