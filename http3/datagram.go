package http3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/quicvarint"
)

var ErrDatagramNegotiationNotFinished = errors.New("the datagram setting negotiation is not finished")

const DatagramRcvQueueLen = 1024

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
		str:         str,
		conn:        m.conn,
		rcvd:        make(chan struct{}),
		ctx:         context.Background(),
		rcvChan:     make(chan []byte, 128),
		timeOutChan: make(chan struct{}),
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
			fmt.Printf("Reading datagram Quarter Stream ID failed: %s\n", err)
			m.logger.Debugf("Reading datagram Quarter Stream ID failed: %s", err)
			continue
		}
		streamID := quarterStreamID * 4
		m.mutex.RLock()
		stream, ok := m.datagrammers[protocol.StreamID(streamID)]
		m.mutex.RUnlock()
		if !ok {
			fmt.Printf("Received datagram for unknown stream: %d\n", streamID)
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

	SetReadTimeOut(t time.Duration)

	GetQuicConn() quic.Connection
}

// streamAssociatedDatagrammer allows sending and receiving HTTP/3 datagrams before the associated quic
// stream is closed
type streamAssociatedDatagrammer struct {
	str  quic.Stream
	conn quic.Connection

	rcvQueue [][]byte
	rcvd     chan struct{}
	rcvChan  chan []byte

	// The time of timeout, each time call SetReadTimeOut should check its value
	readTimeOut time.Time
	// If timeout, send timeout signal
	timeOutChan chan struct{}

	ctx context.Context
}

func (d *streamAssociatedDatagrammer) SetReadTimeOut(t time.Duration) {
	fmt.Println("[debug] Set Read timeout.")
	// set timeout time
	if !d.readTimeOut.IsZero() && d.readTimeOut.Before(time.Now().Add(t)) {
		fmt.Println("[debug] has a earlier timeout time before.")
		return
	}
	// update timeout
	timeout := time.Now().Add(t)
	d.readTimeOut = timeout
	time.Sleep(t)
	fmt.Println("[debug] after sleep!!")
	// If timeout has not be update, send timeout signal
	if timeout.Equal(d.readTimeOut) {
		d.timeOutChan <- struct{}{}
	} else {
		fmt.Println("[debug] timeout has been update, don't sent timeout signal again.")
	}

}

func (d *streamAssociatedDatagrammer) GetQuicConn() quic.Connection {
	return d.conn
}

func (d *streamAssociatedDatagrammer) SendMessage(data []byte) error {
	if !d.conn.ConnectionState().SupportsDatagrams {
		return errors.New("peer doesn't support datagram")
	}

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

	select {
	case data := <-d.rcvChan:
		return data, nil
	case <-d.timeOutChan:
		fmt.Println("[debub] read timeout!")
		return nil, errors.New("timeout")
	}
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
