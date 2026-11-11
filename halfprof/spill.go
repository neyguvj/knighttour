package halfprof

// spill.go — external-merge building blocks: a versioned binary run format,
// the per-group working-directory accounting (spool), and spillTable, a
// profileSink that flushes its aggregation to a sorted run once it grows past
// a byte threshold. One half is enumerated once; its profiles stream through a
// bounded in-memory table onto disk as sorted runs, which external.go merges.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// runMagic and runVersion head every run file so a reader rejects foreign
	// or stale-format data instead of misreading bytes as records.
	runMagic   uint64 = 0x4850_4631_5255_4E00 // ASCII "HPF1RUN\0"
	runVersion byte   = 1
)

// runRef is one written run: its path, byte size and the profile bookkeeping of
// that run (entry count and largest single weight). A lone run already holds a
// complete aggregation, so these are also its final stream's statistics.
type runRef struct {
	path    string
	size    int64
	entries int
	maxW    uint64
}

// spool owns one group's working directory and the disk accounting shared by
// its two halves: live bytes of on-disk runs, their running peak and the count
// of enumeration spills.
type spool struct {
	dir      string
	seq      int
	liveDisk uint64
	peakDisk uint64
	spills   int
}

// newSpool makes a private working directory under base (os.TempDir when base
// is empty). The caller removes it, honouring preserve.
func newSpool(base string) (*spool, error) {
	if base == "" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "halfprof-x-")
	if err != nil {
		return nil, fmt.Errorf("halfprof: working dir: %w", err)
	}
	return &spool{dir: dir}, nil
}

// nextPath hands out a fresh run path for the named side. Enumeration and
// merge of one group are sequential, so the counter needs no synchronisation.
func (sp *spool) nextPath(side string) string {
	sp.seq++
	return filepath.Join(sp.dir, fmt.Sprintf("%s-%06d.run", side, sp.seq))
}

// addRun records a newly written run of the given size in the live/peak totals.
func (sp *spool) addRun(size int64) {
	sp.liveDisk += uint64(size)
	sp.peakDisk = max(sp.peakDisk, sp.liveDisk)
}

// dropRun removes a consumed run's size from the live total.
func (sp *spool) dropRun(size int64) { sp.liveDisk -= uint64(size) }

// remove deletes a run file and its accounting; used as runs are merged away.
func (sp *spool) remove(r runRef) error {
	if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sp.dropRun(r.size)
	return nil
}

// runWriter appends records to one run file behind a buffered writer.
type runWriter struct {
	f *os.File
	w *bufio.Writer
	n int
}

// createRun starts a new run file in sp for the named side, writing the header.
func createRun(sp *spool, side string) (*runWriter, string, error) {
	path := sp.nextPath(side)
	f, err := os.Create(path)
	if err != nil {
		return nil, "", fmt.Errorf("halfprof: create run: %w", err)
	}
	w := bufio.NewWriter(f)
	var hdr [8 + 1]byte
	binary.LittleEndian.PutUint64(hdr[:8], runMagic)
	hdr[8] = runVersion
	if _, err := w.Write(hdr[:]); err != nil {
		return nil, "", errors.Join(fmt.Errorf("halfprof: run header: %w", err), f.Close())
	}
	return &runWriter{f: f, w: w}, path, nil
}

// record appends one (key, weight) row: varint K, varint |P|, P bytes, varint
// weight — the compact form fixed in specs/halfprof.md.
func (rw *runWriter) record(k profKey, wt uint64) error {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], k.K)
	if _, err := rw.w.Write(buf[:n]); err != nil {
		return err
	}
	n = binary.PutUvarint(buf[:], uint64(len(k.P)))
	if _, err := rw.w.Write(buf[:n]); err != nil {
		return err
	}
	if _, err := rw.w.WriteString(k.P); err != nil {
		return err
	}
	n = binary.PutUvarint(buf[:], wt)
	if _, err := rw.w.Write(buf[:n]); err != nil {
		return err
	}
	rw.n++
	return nil
}

// close flushes, sizes the file while still open and closes it.
func (rw *runWriter) close() (int64, error) {
	if err := rw.w.Flush(); err != nil {
		return 0, errors.Join(fmt.Errorf("halfprof: flush run: %w", err), rw.f.Close())
	}
	st, err := rw.f.Stat()
	if err != nil {
		return 0, errors.Join(fmt.Errorf("halfprof: stat run: %w", err), rw.f.Close())
	}
	size := st.Size()
	if err := rw.f.Close(); err != nil {
		return 0, fmt.Errorf("halfprof: close run: %w", err)
	}
	return size, nil
}

// discard closes a run writer whose output is being abandoned (a mid-write
// failure); the partial file is removed by the caller.
func (rw *runWriter) discard() error { return rw.f.Close() }

// runReader reads records back in the order they were written.
type runReader struct {
	f *os.File
	r *bufio.Reader
}

// openRun opens a run file and validates its header.
func openRun(path string) (*runReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("halfprof: open run: %w", err)
	}
	r := bufio.NewReader(f)
	var hdr [8 + 1]byte
	if _, rerr := io.ReadFull(r, hdr[:]); rerr != nil {
		return nil, errors.Join(fmt.Errorf("halfprof: run header read: %w", rerr), f.Close())
	}
	if binary.LittleEndian.Uint64(hdr[:8]) != runMagic || hdr[8] != runVersion {
		return nil, errors.Join(fmt.Errorf("halfprof: bad run format in %s", path), f.Close())
	}
	return &runReader{f: f, r: r}, nil
}

// next returns the following record, or io.EOF past the last one.
func (rr *runReader) next() (profKey, uint64, error) {
	k, err := binary.ReadUvarint(rr.r)
	if err != nil {
		return profKey{}, 0, err // io.EOF at end
	}
	ln, err := binary.ReadUvarint(rr.r)
	if err != nil {
		return profKey{}, 0, err
	}
	pb := make([]byte, ln)
	if _, rerr := io.ReadFull(rr.r, pb); rerr != nil {
		return profKey{}, 0, rerr
	}
	wt, err := binary.ReadUvarint(rr.r)
	if err != nil {
		return profKey{}, 0, err
	}
	return profKey{K: k, P: string(pb)}, wt, nil
}

// close releases the file handle.
func (rr *runReader) close() error { return rr.f.Close() }

// spillTable is a profileSink that spills its map to sorted runs at a byte
// threshold, keeping live memory bounded by that threshold plus one entry.
type spillTable struct {
	err     error
	m       map[profKey]uint64
	sp      *spool
	side    string
	runs    []runRef
	spillAt int64
	used    int64
}

// newSpillTable returns a sink that flushes once its live table would exceed
// spillBytes (charged at entryReserve per distinct key, as the in-memory cap is).
func newSpillTable(side string, sp *spool, spillBytes int) *spillTable {
	return &spillTable{m: make(map[profKey]uint64), sp: sp, side: side, spillAt: int64(spillBytes)}
}

// add folds one observation in, spilling first when a new key would cross the
// threshold so the live map never exceeds it by more than one entry.
func (t *spillTable) add(k profKey, w uint64) {
	if t.err != nil {
		return
	}
	if _, ok := t.m[k]; !ok {
		if t.used+entryReserve > t.spillAt {
			if err := t.flush(); err != nil {
				t.err = err
				return
			}
		}
		t.m[k] = 0
		t.used += entryReserve
	}
	t.m[k] += w
}

// flush sorts the live map and writes it as one run, then clears it.
func (t *spillTable) flush() error {
	if len(t.m) == 0 {
		return nil
	}
	rw, path, err := createRun(t.sp, t.side)
	if err != nil {
		return err
	}
	ref := runRef{path: path, entries: len(t.m)}
	for _, e := range sortedEntries(t.m) {
		if rerr := rw.record(e.k, e.w); rerr != nil {
			return errors.Join(fmt.Errorf("halfprof: write run %s: %w", path, rerr), rw.discard())
		}
		ref.maxW = max(ref.maxW, e.w)
	}
	size, err := rw.close()
	if err != nil {
		return err
	}
	ref.size = size
	t.sp.addRun(size)
	t.sp.spills++ // enumeration spill (merge rewrites are not counted here)
	t.runs = append(t.runs, ref)
	clear(t.m)
	t.used = 0
	return nil
}

// finish flushes any pending rows and returns this half's runs.
func (t *spillTable) finish() ([]runRef, error) {
	if err := t.flush(); err != nil {
		return nil, err
	}
	return t.runs, nil
}

// failure reports a sticky I/O error that should stop enumeration.
func (t *spillTable) failure() error { return t.err }

// spillTable is a profileSink.
var _ profileSink = (*spillTable)(nil)
