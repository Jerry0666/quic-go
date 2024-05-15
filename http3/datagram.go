package http3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/quicvarint"
)

var ErrDatagramNegotiationNotFinished = errors.New("the datagram setting negotiation is not finished")

const DatagramRcvQueueLen = 256

type datagrammerMap struct {
	mutex        sync.RWMutex
	conn         quic.Connection
	datagrammers map[protocol.StreamID]*streamAssociatedDatagrammer
	logger       utils.Logger
}

func newDatagrammerMap(conn quic.Connection, logger utils.Logger) *datagrammerMap {
	m := &datagrammerMap{
		conn:         conn,
		datagrammers: make(map[protocol.StreamID]*streamAssociatedDatagrammer),
		logger:       logger,
	}

	go m.runReceiving()

	return m
}

func (m *datagrammerMap) newStreamAssociatedDatagrammer(str quic.Stream) *streamAssociatedDatagrammer {
	d := &streamAssociatedDatagrammer{
		str:  str,
		conn: m.conn,
		rcvd: make(chan struct{}),
		ctx:  context.Background(),
	}
	m.mutex.Lock()
	m.datagrammers[str.StreamID()] = d
	m.mutex.Unlock()
	go func() {
		<-str.Context().Done()
		m.mutex.Lock()
		delete(m.datagrammers, str.StreamID())
		m.mutex.Unlock()
	}()
	return d
}

func (m *datagrammerMap) runReceiving() {
	j := 0
	for {
		j++
		data, err := m.conn.ReceiveDatagram(context.Background())
		if j%50 == 0 {
			fmt.Printf("j = %d\n", j)
		}
		if err != nil {
			m.logger.Debugf("Stop receiving datagram: %s", err)
			return
		}
		buf := bytes.NewBuffer(data)
		quarterStreamID, err := quicvarint.Read(buf)
		if err != nil {
			m.logger.Debugf("Reading datagram Quarter Stream ID failed: %s", err)
			continue
		}
		streamID := quarterStreamID * 4
		m.mutex.RLock()
		stream, ok := m.datagrammers[protocol.StreamID(streamID)]
		m.mutex.RUnlock()
		if !ok {
			m.logger.Debugf("Received datagram for unknown stream: %d", streamID)
			continue
		}
		stream.handleDatagram(buf.Bytes())
	}
}

// Datagrammer is an interface that can send and receive HTTP datagrams
type Datagrammer interface {
	// SendMessage sends an HTTP Datagram associated with an HTTP request.
	// It must only be called while the send side of the stream is still open, i.e.
	// * on the client side: before calling Close on the request body
	// * on the server side: before calling Close on the response body
	SendMessage([]byte) error
	// SendMessage receives an HTTP Datagram associated with an HTTP request:
	// * on the server side: datagrams can be received while the request handler hasn't returned, AND
	//      the client hasn't close the request stream yet
	// * on the client side: datagrams can be received with the server hasn't close the response stream
	ReceiveMessage(context.Context) ([]byte, error)
}

// streamAssociatedDatagrammer allows sending and receiving HTTP/3 datagrams before the associated quic
// stream is closed
type streamAssociatedDatagrammer struct {
	str  quic.Stream
	conn quic.Connection

	buf      []byte
	mutex    sync.Mutex
	rcvQueue [][]byte
	rcvd     chan struct{}
	front    int

	ctx context.Context
}

func (d *streamAssociatedDatagrammer) SendMessage(data []byte) error {
	if !d.conn.ConnectionState().SupportsDatagrams {
		return errors.New("peer doesn't support datagram")
	}

	d.buf = d.buf[:0]
	d.buf = (&datagramFrame{QuarterStreamID: uint64(d.str.StreamID() / 4)}).Append(d.buf)
	d.buf = append(d.buf, data...)
	return d.conn.SendDatagram(d.buf)
}

func (d *streamAssociatedDatagrammer) ReceiveMessage(ctx context.Context) ([]byte, error) {
	if !d.conn.ConnectionState().SupportsDatagrams {
		return nil, errors.New("peer doesn't support datagram")
	}
	for {
		d.mutex.Lock()
		if len(d.rcvQueue)-d.front > 0 {
			data := d.rcvQueue[d.front]
			d.front++
			d.mutex.Unlock()
			return data, nil
		}
		d.mutex.Unlock()
		select {
		case <-d.rcvd:
			continue
		case <-d.str.Context().Done():
			return nil, fmt.Errorf("the corresponding stream is closed")
		case <-d.ctx.Done():
			return nil, d.ctx.Err()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (d *streamAssociatedDatagrammer) handleDatagram(data []byte) {
	if len(d.rcvQueue) >= DatagramRcvQueueLen && d.front > 0 {
		fmt.Printf("len:%d, front:%d\n", len(d.rcvQueue), d.front)
		d.mutex.Lock()
		d.rcvQueue = d.rcvQueue[d.front:]
		d.front = 0
		d.mutex.Unlock()
	}
	if len(d.rcvQueue) < DatagramRcvQueueLen {
		d.rcvQueue = append(d.rcvQueue, data)
		select {
		case d.rcvd <- struct{}{}:
		default:
		}
	}

}
