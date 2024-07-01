package quic

import (
	"fmt"

	"github.com/quic-go/quic-go/internal/protocol"
)

type sender interface {
	Send(p *packetBuffer, gsoSize uint16, ecn protocol.ECN)
	Send2(p *packetBuffer, gsoSize uint16, ecn protocol.ECN)
	Run() error
	WouldBlock() bool
	Available() <-chan struct{}
	Close()
	SetBackup(conn2 sendConn)
	Migration()
}

type queueEntry struct {
	buf     *packetBuffer
	gsoSize uint16
	ecn     protocol.ECN
}

type sendQueue struct {
	queue       chan queueEntry
	queue2      chan queueEntry
	closeCalled chan struct{} // runStopped when Close() is called
	runStopped  chan struct{} // runStopped when the run loop returns
	available   chan struct{}
	migration   chan struct{} // Used to transmit migration signals
	conn        sendConn
	conn2       sendConn
}

func (h *sendQueue) SetBackup(conn2 sendConn) {
	if conn2 == nil {
		fmt.Println("sendConn is nil")
	}
	h.conn2 = conn2
}

func (h *sendQueue) Migration() {
	if h.conn2 == nil {
		fmt.Println("migration error, conn2 has not been set.")
		return
	}
	h.migration <- struct{}{}
}

var _ sender = &sendQueue{}

const sendQueueCapacity = 8

func newSendQueue(conn sendConn) sender {
	return &sendQueue{
		conn:        conn,
		runStopped:  make(chan struct{}),
		closeCalled: make(chan struct{}),
		available:   make(chan struct{}, 1),
		migration:   make(chan struct{}),
		queue:       make(chan queueEntry, sendQueueCapacity),
		queue2:      make(chan queueEntry, sendQueueCapacity),
	}
}

// Send sends out a packet. It's guaranteed to not block.
// Callers need to make sure that there's actually space in the send queue by calling WouldBlock.
// Otherwise Send will panic.
func (h *sendQueue) Send(p *packetBuffer, gsoSize uint16, ecn protocol.ECN) {
	select {
	case h.queue <- queueEntry{buf: p, gsoSize: gsoSize, ecn: ecn}:
		// clear available channel if we've reached capacity
		if len(h.queue) == sendQueueCapacity {
			select {
			case <-h.available:
			default:
			}
		}
	case <-h.runStopped:
	default:
		panic("sendQueue.Send would have blocked")
	}
}

// Push into queue2
func (h *sendQueue) Send2(p *packetBuffer, gsoSize uint16, ecn protocol.ECN) {
	select {
	case h.queue2 <- queueEntry{buf: p, gsoSize: gsoSize, ecn: ecn}:
		// clear available channel if we've reached capacity
		if len(h.queue2) == sendQueueCapacity {
			fmt.Println("queue2 is full!")
		}
	case <-h.runStopped:
	default:
		panic("sendQueue.Send would have blocked")
	}
}

func (h *sendQueue) WouldBlock() bool {
	return len(h.queue) == sendQueueCapacity
}

func (h *sendQueue) Available() <-chan struct{} {
	return h.available
}

func (h *sendQueue) Run() error {
	defer close(h.runStopped)
	var shouldClose bool
	AlreadyMigrated := false
	for {
		if shouldClose && len(h.queue) == 0 {
			return nil
		}
		select {
		case <-h.migration:
			fmt.Println("Receive migration signal, migrate to conn2.")
			AlreadyMigrated = true
		case <-h.closeCalled:
			h.closeCalled = nil // prevent this case from being selected again
			// make sure that all queued packets are actually sent out
			shouldClose = true
		case e := <-h.queue:
			if AlreadyMigrated {
				err := h.conn2.Write(e.buf.Data, e.gsoSize, e.ecn)
				if err != nil {
					// This additional check enables:
					// 1. Checking for "datagram too large" message from the kernel, as such,
					// 2. Path MTU discovery,and
					// 3. Eventual detection of loss PingFrame.
					if !isSendMsgSizeErr(err) {
						return err
					}
				}
			} else {
				err := h.conn.Write(e.buf.Data, e.gsoSize, e.ecn)
				if err != nil {
					// This additional check enables:
					// 1. Checking for "datagram too large" message from the kernel, as such,
					// 2. Path MTU discovery,and
					// 3. Eventual detection of loss PingFrame.
					if !isSendMsgSizeErr(err) {
						return err
					}
				}
			}
			e.buf.Release()
			select {
			case h.available <- struct{}{}:
			default:
			}
		case e := <-h.queue2:
			fmt.Println("receive from queue2!")
			// temporarily hardcode, an explicit signal is needed
			// to specify the use of the second conn
			if h.conn2 == nil {
				fmt.Println("sendQueue conn2 is nil.")
			}
			err := h.conn2.Write(e.buf.Data, e.gsoSize, e.ecn)
			if err != nil {
				if !isSendMsgSizeErr(err) {
					return err
				}
			}
			e.buf.Release()
			select {
			case h.available <- struct{}{}:
			default:
			}
		}
	}
}

func (h *sendQueue) Close() {
	close(h.closeCalled)
	// wait until the run loop returned
	<-h.runStopped
}
