package quic

import (
	"fmt"
	"net"

	"github.com/quic-go/quic-go/internal/utils"
)

type sender interface {
	Send(p *packetBuffer)
	Run() error
	WouldBlock() bool
	Available() <-chan struct{}
	Close()
}

type sendQueue struct {
	queue         chan *packetBuffer
	queue2        chan *packetBuffer
	closeCalled   chan struct{} // runStopped when Close() is called
	runStopped    chan struct{} // runStopped when the run loop returns
	available     chan struct{}
	Test          bool
	conn          sendConn
	conn2         sendConn
	LocalUdpConn  net.PacketConn
	MigrationSign chan struct{}
	MigrationBool bool
}

var _ sender = &sendQueue{}

const sendQueueCapacity = 8

func UseSecondQueue(s sender, p *packetBuffer) {
	fmt.Println("UseSecondQueue")
	mySendQueue := s.(*sendQueue)
	mySendQueue.Send2(p)
}

func newSendQueue(conn sendConn) sender {
	return &sendQueue{
		conn:          conn,
		runStopped:    make(chan struct{}),
		closeCalled:   make(chan struct{}),
		available:     make(chan struct{}, 1),
		Test:          false,
		queue:         make(chan *packetBuffer, sendQueueCapacity),
		queue2:        make(chan *packetBuffer, sendQueueCapacity),
		MigrationSign: make(chan struct{}),
		MigrationBool: false,
	}
}

func (h *sendQueue) SetSecondConn(conn2 sendConn) {
	h.conn2 = conn2
}

func newSendQueueServer(conn sendConn) sender {
	return &sendQueue{
		conn:          conn,
		runStopped:    make(chan struct{}),
		closeCalled:   make(chan struct{}),
		available:     make(chan struct{}, 1),
		Test:          false,
		queue:         make(chan *packetBuffer, sendQueueCapacity),
		queue2:        make(chan *packetBuffer, sendQueueCapacity),
		MigrationSign: make(chan struct{}),
		MigrationBool: false,
	}
}

func (h *sendQueue) CreateSecondConn(remoteAddr net.Addr) {
	utils.DebugLogEnterfunc("[sendQueue] CreateSecondConn.")
	//udpRemodeAddr, _ := net.ResolveUDPAddr("udp", remoteAddr.String())

}

// Send sends out a packet. It's guaranteed to not block.
// Callers need to make sure that there's actually space in the send queue by calling WouldBlock.
// Otherwise Send will panic.
func (h *sendQueue) Send(p *packetBuffer) {
	select {
	case h.queue <- p:
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

func (h *sendQueue) Send2(p *packetBuffer) {
	fmt.Println("send packet to the second queue!")
	select {
	case h.queue2 <- p:
		// clear available channel if we've reached capacity
		if len(h.queue2) == sendQueueCapacity {
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

func (h *sendQueue) WouldBlock() bool {
	return len(h.queue) == sendQueueCapacity
}

func (h *sendQueue) Available() <-chan struct{} {
	return h.available
}

func (h *sendQueue) Migration() {
	utils.TemporaryLog("[sendQueue] migration")
	h.conn = h.conn2
	utils.TemporaryLog("[sendQueue] close conn2")
	h.conn2.Close()
}

func (h *sendQueue) Run() error {
	defer close(h.runStopped)
	var shouldClose bool
	for {
		if shouldClose && len(h.queue) == 0 {
			return nil
		}
		select {
		case <-h.MigrationSign:
			utils.TemporaryLog("receive migration sign! Do the connection migration.")
			h.Migration()
		case <-h.closeCalled:
			h.closeCalled = nil // prevent this case from being selected again
			// make sure that all queued packets are actually sent out
			shouldClose = true
		case p := <-h.queue2:
			utils.TemporaryLog("receive from queue2!")
			utils.TemporaryLog("local addr:%v", h.conn2.LocalAddr())
			utils.TemporaryLog("remote addr:%v", h.conn2.RemoteAddr())
			err := h.conn2.Write(p.Data)
			if err != nil {
				if !isMsgSizeErr(err) {
					return err
				}
			}
			p.Release()
			select {
			case h.available <- struct{}{}:
			default:
			}
		case p := <-h.queue:
			err := h.conn.Write(p.Data)
			if err != nil {
				// This additional check enables:
				// 1. Checking for "datagram too large" message from the kernel, as such,
				// 2. Path MTU discovery,and
				// 3. Eventual detection of loss PingFrame.
				if !isMsgSizeErr(err) {
					return err
				}
			}
			p.Release()
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
