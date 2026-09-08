package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"unicode/utf8"
)

const MaxFrameBytes uint32 = 8 << 20

var (
	ErrEmptyFrame    = errors.New("empty frame")
	ErrFrameTooLarge = errors.New("frame too large")
	ErrInvalidUTF8   = errors.New("invalid utf-8")
)

type Reader struct {
	source  io.Reader
	maximum uint32
}

func NewReader(source io.Reader, maximum uint32) *Reader {
	if maximum > MaxFrameBytes {
		maximum = MaxFrameBytes
	}
	return &Reader{source: source, maximum: maximum}
}

func (reader *Reader) Read() (Envelope, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(reader.source, prefix[:]); err != nil {
		return Envelope{}, err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 {
		return Envelope{}, ErrEmptyFrame
	}
	if length > reader.maximum {
		return Envelope{}, ErrFrameTooLarge
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader.source, body); err != nil {
		return Envelope{}, err
	}
	if !utf8.Valid(body) {
		return Envelope{}, ErrInvalidUTF8
	}
	return decodeEnvelope(body)
}

type Writer struct {
	destination io.Writer
	mu          sync.Mutex
}

func NewWriter(destination io.Writer) *Writer {
	return &Writer{destination: destination}
}

func (writer *Writer) Write(envelope Envelope) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := envelope.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return ErrEmptyFrame
	}
	if uint64(len(body)) > uint64(MaxFrameBytes) {
		return ErrFrameTooLarge
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	if err := writeFull(writer.destination, prefix[:]); err != nil {
		return err
	}
	return writeFull(writer.destination, body)
}

func writeFull(destination io.Writer, body []byte) error {
	for len(body) > 0 {
		count, err := destination.Write(body)
		if count < 0 || count > len(body) {
			return io.ErrShortWrite
		}
		body = body[count:]
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
