// Package library implements the model library: GGUF parsing, local scanning, Hugging Face lookups,
// memory-fit estimation, a starter catalog and a download queue.
package library

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// Sentinel errors from ParseGGUF.
var (
	ErrNotGGUF     = errors.New("not a GGUF file")
	ErrUnsupported = errors.New("unsupported GGUF version")
	ErrTruncated   = errors.New("GGUF header is truncated")
	ErrTooLarge    = errors.New("GGUF header exceeds the read limit")
	ErrMalformed   = errors.New("malformed GGUF header")
)

const (
	maxKeyLen    = 1 << 12
	maxValueLen  = 1 << 20 // longest string value we keep; longer ones are skipped
	maxKVCount   = 1 << 20
	maxArrayKeep = 1 << 10 // per-layer integer arrays we read (head counts)
	maxDepth     = 4
)

// Metadata is the subset of a GGUF header the library needs.
type Metadata struct {
	Architecture    string `json:"architecture"`
	Name            string `json:"name"`
	FileType        int    `json:"file_type"` // llama_ftype, -1 when absent
	Quant           string `json:"quant"`     // label for FileType, empty when unknown
	ParamCount      uint64 `json:"param_count"`
	BlockCount      int    `json:"block_count"`
	ContextLength   int    `json:"context_length"`
	EmbeddingLength int    `json:"embedding_length"`
	HeadCount       int    `json:"head_count"`
	HeadCountKV     int    `json:"head_count_kv"` // the largest per-layer value when it varies
	KeyLength       int    `json:"key_length"`    // per-head K dimension, 0 when absent
	ValueLength     int    `json:"value_length"`
	ExpertCount     int    `json:"expert_count"`
	TensorCount     uint64 `json:"tensor_count"`
	Alignment       int    `json:"alignment"`
	IsMoE           bool   `json:"is_moe"`
	IsMMProj        bool   `json:"is_mmproj"`
}

// ftypeLabels maps llama_ftype values to their usual names.
var ftypeLabels = map[int]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 7: "Q8_0", 8: "Q5_0", 9: "Q5_1", 10: "Q2_K",
	11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L", 14: "Q4_K_S", 15: "Q4_K_M", 16: "Q5_K_S",
	17: "Q5_K_M", 18: "Q6_K", 19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S", 22: "IQ3_XS",
	23: "IQ3_XXS", 24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M", 28: "IQ2_S", 29: "IQ2_M",
	30: "IQ4_XS", 31: "IQ1_M", 32: "BF16", 36: "TQ1_0", 37: "TQ2_0", 38: "MXFP4",
}

// limitedReader tracks the bytes consumed so hostile headers cannot make us read without bound.
type limitedReader struct {
	r   *bufio.Reader
	pos int64
	max int64
}

func (l *limitedReader) take(n uint64) error {
	if n > uint64(l.max-l.pos) {
		return ErrTooLarge
	}
	return nil
}

func (l *limitedReader) full(p []byte) error {
	if err := l.take(uint64(len(p))); err != nil {
		return err
	}
	if _, err := io.ReadFull(l.r, p); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrTruncated
		}
		return err
	}
	l.pos += int64(len(p))
	return nil
}

func (l *limitedReader) skip(n uint64) error {
	if err := l.take(n); err != nil {
		return err
	}
	// Discard in bounded steps so a lying length on a short stream fails as truncated.
	for n > 0 {
		step := min(n, 1<<20)
		d, err := l.r.Discard(int(step))
		l.pos += int64(d)
		n -= uint64(d)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return ErrTruncated
			}
			return err
		}
	}
	return nil
}

func (l *limitedReader) u32() (uint32, error) {
	var b [4]byte
	if err := l.full(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func (l *limitedReader) u64() (uint64, error) {
	var b [8]byte
	if err := l.full(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

// str reads a string of at most limit bytes; longer ones are skipped and returned as ok=false.
func (l *limitedReader) str(limit uint64) (s string, ok bool, err error) {
	n, err := l.u64()
	if err != nil {
		return "", false, err
	}
	if n > limit {
		return "", false, l.skip(n)
	}
	if err := l.take(n); err != nil {
		return "", false, err
	}
	b := make([]byte, n)
	if err := l.full(b); err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// scalarSize returns the byte size of a fixed-size GGUF value type, or 0.
func scalarSize(t uint32) uint64 {
	switch t {
	case 0, 1, 7:
		return 1
	case 2, 3:
		return 2
	case 4, 5, 6:
		return 4
	case 10, 11, 12:
		return 8
	}
	return 0
}

// readInt reads a fixed-size value of type t, returning it as an integer when it is one.
func (l *limitedReader) readInt(t uint32) (v uint64, isInt bool, err error) {
	var b [8]byte
	n := scalarSize(t)
	if n == 0 {
		return 0, false, ErrMalformed
	}
	if err := l.full(b[:n]); err != nil {
		return 0, false, err
	}
	switch t {
	case 0, 7:
		return uint64(b[0]), true, nil
	case 1:
		return uint64(int64(int8(b[0]))), true, nil
	case 2:
		return uint64(binary.LittleEndian.Uint16(b[:])), true, nil
	case 3:
		return uint64(int64(int16(binary.LittleEndian.Uint16(b[:])))), true, nil
	case 4:
		return uint64(binary.LittleEndian.Uint32(b[:])), true, nil
	case 5:
		return uint64(int64(int32(binary.LittleEndian.Uint32(b[:])))), true, nil
	case 10, 11:
		return binary.LittleEndian.Uint64(b[:]), true, nil
	}
	return 0, false, nil // floats
}

// skipValue skips a value of type t, descending into arrays safely.
func (l *limitedReader) skipValue(t uint32, depth int) error {
	switch {
	case scalarSize(t) > 0:
		return l.skip(scalarSize(t))
	case t == 8:
		n, err := l.u64()
		if err != nil {
			return err
		}
		return l.skip(n)
	case t == 9:
		if depth >= maxDepth {
			return ErrMalformed
		}
		et, err := l.u32()
		if err != nil {
			return err
		}
		n, err := l.u64()
		if err != nil {
			return err
		}
		if sz := scalarSize(et); sz > 0 {
			if n > math.MaxUint64/sz {
				return ErrTooLarge
			}
			return l.skip(n * sz)
		}
		// Each element needs at least 8 bytes, so the read budget bounds this loop.
		for ; n > 0; n-- {
			if err := l.skipValue(et, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return ErrMalformed
}

func toInt(v uint64) int {
	if v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// ParseGGUF reads a GGUF v2 or v3 header from r, consuming at most maxBytes. It never allocates
// more than a bounded amount for hostile input and returns one of the package's sentinel errors.
func ParseGGUF(r io.Reader, maxBytes int64) (Metadata, error) {
	l := &limitedReader{r: bufio.NewReaderSize(r, 64<<10), max: maxBytes}
	m := Metadata{FileType: -1, Alignment: 32}

	var magic [4]byte
	if err := l.full(magic[:]); err != nil {
		if errors.Is(err, ErrTruncated) {
			return m, ErrNotGGUF
		}
		return m, err
	}
	if string(magic[:]) != "GGUF" {
		return m, ErrNotGGUF
	}
	ver, err := l.u32()
	if err != nil {
		return m, err
	}
	if ver != 2 && ver != 3 {
		return m, fmt.Errorf("%w: %d", ErrUnsupported, ver)
	}
	if m.TensorCount, err = l.u64(); err != nil {
		return m, err
	}
	kvCount, err := l.u64()
	if err != nil {
		return m, err
	}
	if kvCount > maxKVCount {
		return m, fmt.Errorf("%w: %d key/value pairs", ErrMalformed, kvCount)
	}

	ints := map[string]uint64{} // keys with an architecture prefix, resolved once it is known
	var general string
	for range kvCount {
		key, ok, err := l.str(maxKeyLen)
		if err != nil {
			return m, err
		}
		if !ok {
			return m, ErrMalformed
		}
		t, err := l.u32()
		if err != nil {
			return m, err
		}
		switch {
		case t == 8:
			s, ok, err := l.str(maxValueLen)
			if err != nil {
				return m, err
			}
			if ok {
				switch key {
				case "general.architecture":
					m.Architecture = s
				case "general.name":
					m.Name = s
				case "general.type":
					general = s
				}
			}
		case t == 9 && wantedKey(key):
			v, ok, err := l.maxOfIntArray()
			if err != nil {
				return m, err
			}
			if ok {
				ints[key] = v
			}
		case t == 9:
			if err := l.skipValue(t, 0); err != nil {
				return m, err
			}
		case scalarSize(t) > 0 && (wantedKey(key) || strings.HasPrefix(key, "general.")):
			v, isInt, err := l.readInt(t)
			if err != nil {
				return m, err
			}
			if !isInt {
				continue
			}
			switch key {
			case "general.file_type":
				m.FileType = toInt(v)
			case "general.parameter_count":
				m.ParamCount = v
			case "general.alignment":
				if a := toInt(v); a > 0 {
					m.Alignment = a
				}
			default:
				ints[key] = v
			}
		default:
			if err := l.skipValue(t, 0); err != nil {
				return m, err
			}
		}
	}

	get := func(suffix string) int { return toInt(ints[m.Architecture+"."+suffix]) }
	m.BlockCount = get("block_count")
	m.ContextLength = get("context_length")
	m.EmbeddingLength = get("embedding_length")
	m.HeadCount = get("attention.head_count")
	m.HeadCountKV = get("attention.head_count_kv")
	m.KeyLength = get("attention.key_length")
	m.ValueLength = get("attention.value_length")
	m.ExpertCount = get("expert_count")
	m.IsMoE = m.ExpertCount > 1
	m.Quant = ftypeLabels[m.FileType]
	m.IsMMProj = general == "mmproj" || m.Architecture == "clip"
	return m, nil
}

// wantedKey reports whether key is an architecture-prefixed integer we resolve after parsing.
func wantedKey(key string) bool {
	for _, s := range [...]string{
		".block_count", ".context_length", ".embedding_length", ".attention.head_count",
		".attention.head_count_kv", ".attention.key_length", ".attention.value_length", ".expert_count",
	} {
		if strings.HasSuffix(key, s) {
			return true
		}
	}
	return false
}

// maxOfIntArray reads an array, returning its largest element if it is a short integer array
// and skipping it otherwise.
func (l *limitedReader) maxOfIntArray() (max uint64, ok bool, err error) {
	et, err := l.u32()
	if err != nil {
		return 0, false, err
	}
	n, err := l.u64()
	if err != nil {
		return 0, false, err
	}
	sz := scalarSize(et)
	if sz == 0 || et == 6 || et == 12 || n > maxArrayKeep {
		if sz > 0 {
			if n > math.MaxUint64/sz {
				return 0, false, ErrTooLarge
			}
			return 0, false, l.skip(n * sz)
		}
		for ; n > 0; n-- {
			if err := l.skipValue(et, 1); err != nil {
				return 0, false, err
			}
		}
		return 0, false, nil
	}
	for ; n > 0; n-- {
		v, _, err := l.readInt(et)
		if err != nil {
			return 0, false, err
		}
		if int64(v) > 0 && v > max {
			max = v
		}
	}
	return max, true, nil
}
