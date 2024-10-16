package quic

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/utils/ringbuffer"
	"github.com/quic-go/quic-go/internal/wire"
)

const (
	maxDatagramSendQueueLen = 512
	maxDatagramRcvQueueLen  = 512
)

type datagramQueue struct {
	sendMx    sync.Mutex
	sendQueue ringbuffer.RingBuffer[*wire.DatagramFrame]
	sent      chan struct{} // used to notify Add that a datagram was dequeued

	rcvMx    sync.Mutex
	rcvQueue [][]byte
	rcvd     chan struct{} // used to notify Receive that a new datagram was received

	closeErr error
	closed   chan struct{}

	hasData  func()
	sendChan chan *wire.DatagramFrame
	recvChan chan []byte

	// PopfromChan function call number
	PopNumber int

	logger utils.Logger
}

func newDatagramQueue(hasData func(), logger utils.Logger) *datagramQueue {
	d := &datagramQueue{
		hasData:   hasData,
		rcvd:      make(chan struct{}, 1),
		sent:      make(chan struct{}, 1),
		closed:    make(chan struct{}),
		sendChan:  make(chan *wire.DatagramFrame, 1024),
		recvChan:  make(chan []byte, 2048),
		logger:    logger,
		PopNumber: 0,
	}
	// go d.SleepAndLog(40 * time.Second)
	return d
}

// Use chan to manage datagram frame
func (h *datagramQueue) AddtoChan(f *wire.DatagramFrame) error {
	if len(h.sendChan) < cap(h.sendChan) {
		h.sendChan <- f
		h.hasData()
		return nil
	} else {
		return errors.New("datagramQueue sendChan is full")
	}
}

// Sleep some time then log something, should be call in a go routine.
func (h *datagramQueue) SleepAndLog(t time.Duration) {
	fmt.Printf("[log][datagramQueue] log in %d second.\n", t/time.Second)
	time.Sleep(t)
	// log something
	fmt.Printf("[log][datagramQueue] the number of PopfromChan func call:%d\n", h.PopNumber)
}

func (h *datagramQueue) PopfromChan() (*wire.DatagramFrame, error) {
	h.PopNumber++
	f := <-h.sendChan
	return f, nil
}

// Add queues a new DATAGRAM frame for sending.
// Up to 32 DATAGRAM frames will be queued.
// Once that limit is reached, Add blocks until the queue size has reduced.
func (h *datagramQueue) Add(f *wire.DatagramFrame) error {
	h.sendMx.Lock()

	for {
		if h.sendQueue.Len() < maxDatagramSendQueueLen {
			h.sendQueue.PushBack(f)
			h.sendMx.Unlock()
			h.hasData()
			return nil
		}
		select {
		case <-h.sent: // drain the queue so we don't loop immediately
		default:
		}
		h.sendMx.Unlock()
		select {
		case <-h.closed:
			return h.closeErr
		case <-h.sent:
		}
		h.sendMx.Lock()
	}
}

// Peek gets the next DATAGRAM frame for sending.
// If actually sent out, Pop needs to be called before the next call to Peek.
func (h *datagramQueue) Peek() *wire.DatagramFrame {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	if h.sendQueue.Empty() {
		return nil
	}
	return h.sendQueue.PeekFront()
}

func (h *datagramQueue) Pop() {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	_ = h.sendQueue.PopFront()
	select {
	case h.sent <- struct{}{}:
	default:
	}
}

// HandleDatagramFrame handles a received DATAGRAM frame.
func (h *datagramQueue) HandleDatagramFrame(f *wire.DatagramFrame) {
	data := make([]byte, len(f.Data))
	copy(data, f.Data)
	var queued bool
	// use chan
	if len(h.recvChan) < cap(h.recvChan) {
		queued = true
		h.recvChan <- data
	} else {
		fmt.Println("[error] datagram receive chan is full")
	}

	if !queued && h.logger.Debug() {
		h.logger.Debugf("Discarding received DATAGRAM frame (%d bytes payload)", len(f.Data))
	}
}

// Receive gets a received DATAGRAM frame.
func (h *datagramQueue) Receive(ctx context.Context) ([]byte, error) {
	data := <-h.recvChan
	return data, nil
}

func (h *datagramQueue) CloseWithError(e error) {
	h.closeErr = e
	close(h.closed)
}
