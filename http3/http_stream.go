package http3

import (
	"errors"
	"fmt"

	"github.com/quic-go/quic-go"
)

// A Stream is a HTTP/3 stream.
// When writing to and reading from the stream, data is framed in HTTP/3 DATA frames.
type Stream interface {
	quic.Stream

	// Datagrammer returns the Datagrammer implementation to send and receive
	// the datagram payloads according RFC 9297.
	//
	// If the underline connection does not enabled datagram or the peer
	// does not support, this function will return (nil, false).
	Datagrammer() (Datagrammer, bool)
}

// The stream conforms to the quic.Stream interface, but instead of writing to and reading directly
// from the QUIC stream, it writes to and reads from the HTTP stream.
type stream struct {
	quic.Stream

	conn *connection

	buf []byte

	onFrameError          func()
	bytesRemainingInFrame uint64
}

var _ Stream = &stream{}

func newStream(conn *connection, str quic.Stream, onFrameError func()) *stream {
	return &stream{
		Stream:       str,
		conn:         conn,
		onFrameError: onFrameError,
		buf:          make([]byte, 0, 16),
	}
}

func (s *stream) GetConn() quic.Connection {
	return s.conn.Connection
}

func (s *stream) Read(b []byte) (int, error) {
	if s.bytesRemainingInFrame == 0 {
	parseLoop:
		for {
			frame, err := parseNextFrame(s.Stream, nil)
			if err != nil {
				return 0, err
			}
			switch f := frame.(type) {
			case *headersFrame:
				// skip HEADERS frames
				continue
			case *dataFrame:
				s.bytesRemainingInFrame = f.Length
				break parseLoop
			default:
				s.onFrameError()
				// parseNextFrame skips over unknown frame types
				// Therefore, this condition is only entered when we parsed another known frame type.
				return 0, fmt.Errorf("peer sent an unexpected frame: %T", f)
			}
		}
	}

	var n int
	var err error
	if s.bytesRemainingInFrame < uint64(len(b)) {
		n, err = s.Stream.Read(b[:s.bytesRemainingInFrame])
	} else {
		n, err = s.Stream.Read(b)
	}
	s.bytesRemainingInFrame -= uint64(n)
	return n, err
}

func (s *stream) hasMoreData() bool {
	return s.bytesRemainingInFrame > 0
}

func (s *stream) Write(b []byte) (int, error) {
	s.buf = s.buf[:0]
	s.buf = (&dataFrame{Length: uint64(len(b))}).Append(s.buf)
	if _, err := s.Stream.Write(s.buf); err != nil {
		return 0, err
	}
	return s.Stream.Write(b)
}

func (s *stream) Datagrammer() (Datagrammer, bool) {
	d := s.conn.Datagrammer(s.Stream)
	return d, d != nil
}

var errTooMuchData = errors.New("peer sent too much data")

type lengthLimitedStream struct {
	*stream
	contentLength int64
	read          int64
	resetStream   bool
}

var _ Stream = &lengthLimitedStream{}

func newLengthLimitedStream(str *stream, contentLength int64) *lengthLimitedStream {
	return &lengthLimitedStream{
		stream:        str,
		contentLength: contentLength,
	}
}

func (s *lengthLimitedStream) checkContentLengthViolation() error {
	if s.read > s.contentLength || s.read == s.contentLength && s.hasMoreData() {
		if !s.resetStream {
			s.CancelRead(quic.StreamErrorCode(ErrCodeMessageError))
			s.CancelWrite(quic.StreamErrorCode(ErrCodeMessageError))
			s.resetStream = true
		}
		return errTooMuchData
	}
	return nil
}

func (s *lengthLimitedStream) Read(b []byte) (int, error) {
	if err := s.checkContentLengthViolation(); err != nil {
		return 0, err
	}
	n, err := s.stream.Read(b[:min(int64(len(b)), s.contentLength-s.read)])
	s.read += int64(n)
	if err := s.checkContentLengthViolation(); err != nil {
		return n, err
	}
	return n, err
}
