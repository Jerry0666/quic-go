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

const DatagramRcvQueueLen = 128

type datagrammerMap struct {
	mutex        sync.RWMutex
	conn         quic.Connection
	datagrammers map[protocol.StreamID]*streamAssociatedDatagrammer
	logger       utils.Logger
}

func newDatagrammerMap(conn quic.Connection, logger utils.Logger) *datagrammerMap {
	fmt.Println("newDatagrammerMap")
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
		str:     str,
		conn:    m.conn,
		rcvd:    make(chan struct{}),
		ctx:     context.Background(),
		rcvChan: make(chan []byte, 128),
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
	for {
		data, err := m.conn.ReceiveDatagram(context.Background())
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
	ReceiveMessage() ([]byte, error)

	HardcodedRead(ctx context.Context) ([]byte, error)

	GetQuicConn() quic.Connection
}

// streamAssociatedDatagrammer allows sending and receiving HTTP/3 datagrams before the associated quic
// stream is closed
type streamAssociatedDatagrammer struct {
	str  quic.Stream
	conn quic.Connection

	buf      []byte
	rcvQueue [][]byte
	rcvd     chan struct{}
	rcvChan  chan []byte

	ctx context.Context
}

func (d *streamAssociatedDatagrammer) GetQuicConn() quic.Connection {
	return d.conn
}

func (d *streamAssociatedDatagrammer) SendMessage(data []byte) error {
	if !d.conn.ConnectionState().SupportsDatagrams {
		return errors.New("peer doesn't support datagram")
	}

	// d.buf = d.buf[:0]
	// d.buf = (&datagramFrame{QuarterStreamID: uint64(d.str.StreamID() / 4)}).Append(d.buf)
	// d.buf = append(d.buf, data...)
	strID := d.str.StreamID()
	if strID > 63 {
		fmt.Println("stream id is bigger than 63, so length byte is more than one.")
	}
	lenByte := byte(strID / 4)
	sendData := make([]byte, 0)
	sendData = append(sendData, lenByte)
	sendData = append(sendData, data...)
	return d.conn.SendDatagram(sendData)
}

func (d *streamAssociatedDatagrammer) HardcodedRead(ctx context.Context) ([]byte, error) {
	data, err := d.conn.ReceiveDatagram(ctx)
	return data, err
}

func (d *streamAssociatedDatagrammer) ReceiveMessage() ([]byte, error) {
	if !d.conn.ConnectionState().SupportsDatagrams {
		return nil, errors.New("peer doesn't support datagram")
	}
	data := <-d.rcvChan
	return data, nil
}

func (d *streamAssociatedDatagrammer) handleDatagram(data []byte) {

	if len(d.rcvQueue) < DatagramRcvQueueLen {
		d.rcvChan <- data
		select {
		case d.rcvd <- struct{}{}:
		default:
		}
	} else {
		fmt.Println("rcv Queue is full")
	}

}
