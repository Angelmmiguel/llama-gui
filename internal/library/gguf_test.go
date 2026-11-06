package library

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"
)

type ggufBuilder struct {
	buf bytes.Buffer
	n   uint64
}

func (b *ggufBuilder) u32(v uint32) { _ = binary.Write(&b.buf, binary.LittleEndian, v) }
func (b *ggufBuilder) u64(v uint64) { _ = binary.Write(&b.buf, binary.LittleEndian, v) }
func (b *ggufBuilder) str(s string) { b.u64(uint64(len(s))); b.buf.WriteString(s) }
func (b *ggufBuilder) key(k string, t uint32) {
	b.n++
	b.str(k)
	b.u32(t)
}
func (b *ggufBuilder) kvStr(k, v string) { b.key(k, 8); b.str(v) }
func (b *ggufBuilder) kvU32(k string, v uint32) {
	b.key(k, 4)
	b.u32(v)
}
func (b *ggufBuilder) bytes(version uint32, tensors uint64) []byte {
	var out bytes.Buffer
	out.WriteString("GGUF")
	_ = binary.Write(&out, binary.LittleEndian, version)
	_ = binary.Write(&out, binary.LittleEndian, tensors)
	_ = binary.Write(&out, binary.LittleEndian, b.n)
	out.Write(b.buf.Bytes())
	return out.Bytes()
}

func sampleGGUF(version uint32) []byte {
	b := &ggufBuilder{}
	b.kvStr("general.architecture", "qwen3moe")
	b.kvStr("general.name", "Test Model")
	b.kvU32("general.file_type", 15)
	b.key("general.parameter_count", 10)
	b.u64(30_500_000_000)
	b.key("tokenizer.ggml.tokens", 9) // string array skipped
	b.u32(8)
	b.u64(2)
	b.str("a")
	b.str("bb")
	b.key("tokenizer.ggml.scores", 9) // float array skipped
	b.u32(6)
	b.u64(3)
	b.buf.Write(make([]byte, 12))
	b.kvU32("qwen3moe.block_count", 48)
	b.kvU32("qwen3moe.context_length", 262144)
	b.kvU32("qwen3moe.embedding_length", 2048)
	b.kvU32("qwen3moe.attention.head_count", 32)
	b.key("qwen3moe.attention.head_count_kv", 9) // per-layer array
	b.u32(4)
	b.u64(2)
	b.u32(2)
	b.u32(4)
	b.kvU32("qwen3moe.expert_count", 128)
	b.key("general.alignment", 4)
	b.u32(64)
	return b.bytes(version, 579)
}

func TestParseGGUF(t *testing.T) {
	for _, v := range []uint32{2, 3} {
		m, err := ParseGGUF(bytes.NewReader(sampleGGUF(v)), 1<<20)
		if err != nil {
			t.Fatalf("v%d: %v", v, err)
		}
		want := Metadata{
			Architecture: "qwen3moe", Name: "Test Model", FileType: 15, Quant: "Q4_K_M",
			ParamCount: 30_500_000_000, BlockCount: 48, ContextLength: 262144, EmbeddingLength: 2048,
			HeadCount: 32, HeadCountKV: 4, ExpertCount: 128, IsMoE: true, TensorCount: 579, Alignment: 64,
		}
		if m != want {
			t.Errorf("v%d: got %+v\nwant %+v", v, m, want)
		}
	}
}

func TestParseGGUFMMProj(t *testing.T) {
	b := &ggufBuilder{}
	b.kvStr("general.architecture", "clip")
	m, err := ParseGGUF(bytes.NewReader(b.bytes(3, 0)), 1<<20)
	if err != nil || !m.IsMMProj {
		t.Fatalf("clip: %+v %v", m, err)
	}
	b = &ggufBuilder{}
	b.kvStr("general.architecture", "llama")
	b.kvStr("general.type", "mmproj")
	m, err = ParseGGUF(bytes.NewReader(b.bytes(3, 0)), 1<<20)
	if err != nil || !m.IsMMProj {
		t.Fatalf("type: %+v %v", m, err)
	}
}

func TestParseGGUFErrors(t *testing.T) {
	good := sampleGGUF(3)
	huge := func(build func(*ggufBuilder)) []byte {
		b := &ggufBuilder{}
		build(b)
		return b.bytes(3, 0)
	}
	badVersion := append([]byte(nil), good...)
	badVersion[4] = 9
	cases := []struct {
		name string
		data []byte
		max  int64
		want error
	}{
		{"empty", nil, 1 << 20, ErrNotGGUF},
		{"magic", []byte("GGML\x03\x00\x00\x00"), 1 << 20, ErrNotGGUF},
		{"version", badVersion, 1 << 20, ErrUnsupported},
		{"truncated", good[:len(good)-10], 1 << 20, ErrTruncated},
		{"over limit", good, 100, ErrTooLarge},
		{"huge string", huge(func(b *ggufBuilder) { b.key("general.name", 8); b.u64(1 << 62) }), 1 << 20, ErrTooLarge},
		{"huge array", huge(func(b *ggufBuilder) { b.key("x", 9); b.u32(4); b.u64(1 << 60) }), 1 << 20, ErrTooLarge},
		{"huge string array", huge(func(b *ggufBuilder) { b.key("x", 9); b.u32(8); b.u64(1 << 60); b.u64(0) }), 1 << 20, ErrTruncated},
		{"bad type", huge(func(b *ggufBuilder) { b.key("x", 99) }), 1 << 20, ErrMalformed},
		{"nested arrays", huge(func(b *ggufBuilder) {
			b.key("x", 9)
			for range 10 {
				b.u32(9)
				b.u64(1)
			}
		}), 1 << 20, ErrMalformed},
	}
	for _, c := range cases {
		_, err := ParseGGUF(bytes.NewReader(c.data), c.max)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	// A kv_count beyond the cap is rejected before any allocation.
	var hdr bytes.Buffer
	hdr.WriteString("GGUF")
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(3))
	_ = binary.Write(&hdr, binary.LittleEndian, uint64(0))
	_ = binary.Write(&hdr, binary.LittleEndian, uint64(1<<40))
	if _, err := ParseGGUF(&hdr, 1<<20); !errors.Is(err, ErrMalformed) {
		t.Errorf("kv count: %v", err)
	}
}

func FuzzParseGGUF(f *testing.F) {
	f.Add(sampleGGUF(3))
	f.Add(sampleGGUF(2))
	f.Add([]byte("GGUF"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseGGUF(bytes.NewReader(data), 1<<20)
	})
}

const realSmolVLM = "/tmp/claude-1000/-home-angel-Workspace-llama-gui/54c06129-5c81-497f-8adc-120bc33c2187/scratchpad/real/models/SmolVLM-256M-Instruct-Q8_0/SmolVLM-256M-Instruct-Q8_0.gguf"

func TestParseRealGGUF(t *testing.T) {
	f, err := os.Open(realSmolVLM)
	if err != nil {
		t.Skip("real model file not present")
	}
	defer f.Close()
	m, err := ParseGGUF(f, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", m)
	if m.Architecture != "llama" || m.Quant != "Q8_0" || m.IsMMProj || m.BlockCount == 0 || m.EmbeddingLength == 0 {
		t.Errorf("unexpected metadata: %+v", m)
	}
}

func TestScanMTP(t *testing.T) {
	file := func(arch string, keys func(b *ggufBuilder)) []byte {
		b := &ggufBuilder{}
		b.kvStr("general.architecture", arch)
		keys(b)
		return b.bytes(3, 1)
	}
	for name, tc := range map[string]struct {
		data []byte
		want MTP
	}{
		"heads":     {file("qwen35", func(b *ggufBuilder) { b.kvU32("qwen35.nextn_predict_layers", 1) }), MTPHeads},
		"companion": {file("gemma4-assistant", func(b *ggufBuilder) { b.kvU32("gemma4-assistant.nextn_predict_layers", 4) }), MTPCompanion},
		"none":      {file("qwen35", func(b *ggufBuilder) { b.kvU32("qwen35.block_count", 12) }), MTPNone},
		"zero":      {file("qwen35", func(b *ggufBuilder) { b.kvU32("qwen35.nextn_predict_layers", 0) }), MTPNone},
		"stops at the tokenizer": {file("qwen35", func(b *ggufBuilder) {
			b.kvStr("tokenizer.ggml.model", "gpt2")
			b.kvU32("qwen35.nextn_predict_layers", 1)
		}), MTPNone},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ScanMTP(bytes.NewReader(tc.data), int64(len(tc.data)))
			if err != nil || got != tc.want {
				t.Errorf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
	if _, err := ScanMTP(strings.NewReader("nope"), 4); !errors.Is(err, ErrNotGGUF) {
		t.Errorf("not a GGUF: %v", err)
	}
	truncated := file("qwen35", func(b *ggufBuilder) { b.kvU32("qwen35.nextn_predict_layers", 1) })
	if _, err := ScanMTP(bytes.NewReader(truncated[:20]), 1<<20); !errors.Is(err, ErrTruncated) {
		t.Errorf("truncated: %v", err)
	}
}

func TestParseGGUFReadsMTPLayers(t *testing.T) {
	b := &ggufBuilder{}
	b.kvStr("general.architecture", "qwen35")
	b.kvU32("qwen35.nextn_predict_layers", 2)
	data := b.bytes(3, 1)
	m, err := ParseGGUF(bytes.NewReader(data), int64(len(data)))
	if err != nil || m.MTPLayers != 2 {
		t.Errorf("%+v %v", m, err)
	}
}

func FuzzScanMTP(f *testing.F) {
	f.Add(sampleGGUF(3))
	f.Add([]byte("GGUF"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ScanMTP(bytes.NewReader(data), int64(len(data)))
	})
}
