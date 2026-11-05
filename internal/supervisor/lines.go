package supervisor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// maxLine bounds what one log line can make us hold.
const maxLine = 1 << 20

// scanLines calls emit for every line in r, without the line ending. Lines longer
// than limit are cut there and the rest discarded.
func scanLines(r io.Reader, limit int, emit func(string)) {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if room := limit - len(buf); room > 0 {
			buf = append(buf, chunk[:min(room, len(chunk))]...)
		}
		switch {
		case err == nil:
			emit(string(bytes.TrimRight(buf, "\r\n")))
			buf = buf[:0]
		case errors.Is(err, bufio.ErrBufferFull):
		default:
			if len(buf) > 0 {
				emit(string(bytes.TrimRight(buf, "\r\n")))
			}
			return
		}
	}
}
