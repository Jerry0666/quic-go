package quic

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
)

type sender interface {
	Send(p *packetBuffer, gsoSize uint16, ecn protocol.ECN)
	Run() error
	WouldBlock() bool
	Available() <-chan struct{}
	Close()
	Migration(conn sendConn)
}

type queueEntry struct {
	buf     *packetBuffer
	gsoSize uint16
	ecn     protocol.ECN
}

type sendQueue struct {
	queue         chan queueEntry
	closeCalled   chan struct{} // runStopped when Close() is called
	runStopped    chan struct{} // runStopped when the run loop returns
	available     chan struct{}
	migration     chan struct{} // Used to transmit migration signals
	migrationConn chan sendConn // Used to transmit migration conn
	conn          sendConn
	// the number of packet receive from the queue
	sendNumber int
	// the slice used to record send cycle time (nanosecond).
	sendCycle []int64
}

func (h *sendQueue) Migration(conn sendConn) {
	h.migration <- struct{}{}
	h.migrationConn <- conn
}

var _ sender = &sendQueue{}

const sendQueueCapacity = 8

func newSendQueue(conn sendConn) sender {
	return &sendQueue{
		conn:          conn,
		runStopped:    make(chan struct{}),
		closeCalled:   make(chan struct{}),
		available:     make(chan struct{}, 1),
		migration:     make(chan struct{}),
		migrationConn: make(chan sendConn),
		queue:         make(chan queueEntry, sendQueueCapacity),
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

func (h *sendQueue) WouldBlock() bool {
	return len(h.queue) == sendQueueCapacity
}

func (h *sendQueue) Available() <-chan struct{} {
	return h.available
}

// sleep some time and then log
func (h *sendQueue) sleepAndLog(t time.Duration) {
	fmt.Printf("[log][sendQueue] write to CSV in %d second\n", t/time.Second)
	time.Sleep(t)
	fmt.Printf("[log][sendQueue] the total packets received in the queue:%d\n", h.sendNumber)
	fmt.Println("[log][sendQueue] Write cycle time to CSV...")
	file, err := os.OpenFile("/home/allen/excel/sendCycle.csv", os.O_WRONLY, 0777)
	if err != nil {
		fmt.Printf("open csv file err:%v\n", err)
	}
	w := csv.NewWriter(file)
	w.Write([]string{"cycle time"})
	for i := 0; i < 5000; i++ {
		row := make([]string, 1)
		row[0] = strconv.Itoa(int(h.sendCycle[i]))
		w.Write(row)
	}
	w.Flush()
}

func (h *sendQueue) Run() error {
	fmt.Println("[debug] sendQueue Run()")
	defer close(h.runStopped)
	var shouldClose bool
	h.sendNumber = 0
	go h.sleepAndLog(60 * time.Second)

	h.sendCycle = make([]int64, 5000)
	var LastTime, t1 time.Time
	LastTime = time.Now()
	i := 0
	for {
		if shouldClose && len(h.queue) == 0 {
			return nil
		}
		select {
		case <-h.migration:
			conn := <-h.migrationConn
			if conn == nil {
				fmt.Println("[sendQueue] receive the migration signal at the server side.")
			} else {
				fmt.Println("[sendQueue] receive the migration signal, and conn is not nil.")
				h.conn = conn
			}

		case <-h.closeCalled:
			h.closeCalled = nil // prevent this case from being selected again
			// make sure that all queued packets are actually sent out
			shouldClose = true
		case e := <-h.queue:
			if len(e.buf.Data) > 1200 {
				if h.sendNumber > 100 {
					// record cycle time
					t1 = time.Now()
					if i < 5000 {
						// Cycle[0] should be removed, because LastTime has not been set.
						h.sendCycle[i] = int64(t1.Sub(LastTime))
						i++
					}
					LastTime = time.Now()
				}
				h.sendNumber++
			}
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
