package quic

import (
	"crypto/rand"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
)

// PathStatus is the status of a path.
type PathStatus int

const (
	// PathStatusProbing means that the path is being probed (i.e. a PATH_CHALLENGE frame has been sent).
	PathStatusProbing PathStatus = iota
	// PathStatusTimeout means that path probing ran into a timeout,
	// or that a previously successfully probed path was abandoned.
	PathStatusDead
	// PathStatusProbeSuccess means that path probing succeeded. It is now possible to switch to this path.
	PathStatusAlive
	// PathStatusActive means that this is the path that’s used to send QUIC packets
	PathStatusActive
	// Used to check whether Active path is alive or not.
	PathStatusActiveProbing
)

// Path is a network path.
type Path struct {
	// Status is the status of this path.
	Status PathStatus
	// Notify is a channel (cap 1) that a new value is added to every time the status changes.
	// Note that the status can change even after path validation succeeded (e.g. because a path times out).
	Notify <-chan struct{}
	// might add some path characteristics (RTT, MTU, loss rate, etc.) here later

	// Indicate whether this path is the ATSSS Active Path (in Active-Standy Steering mode).
	ATSSSActivePath bool

	// need to use Transpor to listen
	Tr *Transport

	// for the send function
	queue chan queueEntry

	// used to set sendConn
	Remote net.Addr

	// This Path is for the client or server
	perspective protocol.Perspective

	// conn
	udpConn *net.UDPConn
	// for the listen
	Rconn rawConn
	// for the send
	SendConn sendConn

	// conn id
	connId protocol.ConnectionID

	challengeData [8]byte
	// receive conn id
	receiveConnId *ConnectionID

	// the last pathchallenge send time
	LastSendTime time.Time
	RTT          *utils.RTTStats
}

func NewPath(T *Transport, remoteAddr net.Addr, Isclient bool) *Path {
	var per protocol.Perspective
	if Isclient {
		per = protocol.PerspectiveClient
	} else {
		per = protocol.PerspectiveServer
	}

	// generate the random challenge data
	challenge := make([]byte, 8)
	rand.Read(challenge)

	p := &Path{
		queue:         make(chan queueEntry, sendQueueCapacity),
		Tr:            T,
		Remote:        remoteAddr,
		perspective:   per,
		challengeData: [8]byte(challenge),
	}
	fmt.Printf("[Path] generate the random challenge data:%x\n", p.challengeData)
	p.RTT = utils.NewRTTStats()
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
		// new path don't use transport to listen, use path to listen and transfer the packet to transport to handle.
		go p.listen(p.Rconn)
	}
	go p.Run()

	p.SendConn = newSendConn(conn, p.Remote, packetInfo{}, utils.DefaultLogger)
}

// copy from transport.listen
func (path *Path) listen(conn rawConn) {
	fmt.Printf("[Path] listen on %s\n", conn.LocalAddr().String())
	// not do getMultiplexer().AddConn, if needed, add it.
	// so no need to do getMultiplexer().RemoveConn
	defer fmt.Println("[Path] listen close!")
	for {
		p, err := conn.ReadPacket()
		if err != nil {
			// Windows returns an error when receiving a UDP datagram that doesn't fit into the provided buffer.
			if isRecvMsgSizeErr(err) {
				continue
			}
			fmt.Printf("[Path][listen] err:%v\n", err)
			return
		}

		if path.receiveConnId == nil {
			fmt.Println("[Path] receiveConnId is nil, set it.")
			fmt.Printf("connIDLen:%d\n", path.Tr.connIDLen)
			connID, err := wire.ParseConnectionID(p.data, path.Tr.connIDLen)
			if err != nil {
				fmt.Printf("[Path] ParseConnectionId err:%v\n", err)
			}
			fmt.Printf("[Path] receive ConnId:%s\n", connID.String())
			path.receiveConnId = &connID
		}

		// this path is not using now, mark this packet is from other path
		if path.Status != PathStatusActive {
			p.otherPath = true
		}
		path.Tr.handlePacket(p)
	}
}

func (p *Path) Send(pa *packetBuffer, gsoSize uint16, ecn protocol.ECN) {
	p.queue <- queueEntry{buf: pa, gsoSize: 0, ecn: ecn}
}

// run the path send for loop
func (p *Path) Run() error {
	fmt.Println("[Path] run()")
	for {
		e := <-p.queue
		fmt.Println("[Path] receive from queue")
		go func() {
			err := p.SendConn.Write(e.buf.Data, e.gsoSize, e.ecn)
			if err != nil {
				fmt.Printf("[Path] SendConn Write err:%v\n", err)
			}
		}()

	}
}
