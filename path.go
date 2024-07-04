package quic

import (
	"fmt"
	"net"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

// PathStatus is the status of a path.
type PathStatus int

const (
	// PathStatusProbing means that the path is being probed (i.e. a PATH_CHALLENGE frame has been sent).
	PathStatusProbing PathStatus = iota
	// PathStatusTimeout means that path probing ran into a timeout,
	// or that a previously successfully probed path was abandoned.
	PathStatusTimeout
	// PathStatusProbeSuccess means that path probing succeeded. It is now possible to switch to this path.
	PathStatusProbeSuccess
	// PathStatusActive means that this is the path that’s used to send QUIC packets
	PathStatusActive
)

// Path is a network path.
type Path struct {
	// Status is the status of this path.
	Status PathStatus
	// Notify is a channel (cap 1) that a new value is added to every time the status changes.
	// Note that the status can change even after path validation succeeded (e.g. because a path times out).
	Notify <-chan struct{}
	// might add some path characteristics (RTT, MTU, loss rate, etc.) here later

	// need to use Transpor to listen
	Tr *Transport

	// for the send function
	queue chan queueEntry

	Remote net.Addr

	// This Path is for the client or server
	perspective protocol.Perspective

	// conn
	udpConn *net.UDPConn
	// for the listen
	Rconn rawConn
	// for the send
	SendConn sendConn
}

func NewPath(T *Transport, remoteAddr net.Addr, Isclient bool) *Path {
	var per protocol.Perspective
	if Isclient {
		per = protocol.PerspectiveClient
	} else {
		per = protocol.PerspectiveServer
	}

	p := &Path{
		queue:       make(chan queueEntry, sendQueueCapacity),
		Tr:          T,
		Remote:      remoteAddr,
		perspective: per,
	}
	return p

}

func (p *Path) ServerSet(r rawConn, packet receivedPacket) {
	fmt.Println("[Path] server set the Path")
	p.SendConn = newSendConn(r, packet.remoteAddr, packet.info, utils.DefaultLogger)
	p.Rconn = r
	fmt.Println("[Path] run the server path")
	go p.Run()
}

// use in the client side
func (p *Path) SetIP(ip string, port int) {
	fmt.Println("[Path] Setting IP")
	localIP := net.ParseIP(ip)
	if localIP == nil {
		fmt.Println("laddr ip format is wrong, so localIP is nil")
		port = 0
	}
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: localIP, Port: port})
	if err != nil {
		fmt.Println("net.ListenUDP error")
	}
	p.udpConn = udpConn

	var conn rawConn
	fmt.Println("[Path] wrapConn")
	conn, err = wrapConn(udpConn)
	if err != nil {
		fmt.Printf("[Path] wrapConn err:%v\n", err)
	}
	p.Rconn = conn
	// listen on it
	if p.Tr == nil {
		if p.perspective == protocol.PerspectiveClient {
			fmt.Println("[Path] err: Transport is nil, can't use Transport listen.")
		}
	} else {
		fmt.Println("[Path] listen on new rawConn")
		go p.Tr.listen(p.Rconn)
	}
	go p.Run()

	p.SendConn = newSendConn(conn, p.Remote, packetInfo{}, utils.DefaultLogger)
}

func (p *Path) Send(pa *packetBuffer, gsoSize uint16, ecn protocol.ECN) {
	p.queue <- queueEntry{buf: pa, gsoSize: gsoSize, ecn: ecn}
}

func (p *Path) Run() error {
	for {
		e := <-p.queue
		fmt.Println("[Path] receive from queue")
		err := p.SendConn.Write(e.buf.Data, e.gsoSize, e.ecn)
		if err != nil {
			fmt.Printf("[Path] SendConn Write err:%v\n", err)
		}
	}
}
