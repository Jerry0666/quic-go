package pathmanager

import (
	"net"
	"strings"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

type receivedRawPacket struct {
	rcvPconn   net.PacketConn
	remoteAddr net.Addr
	data       []byte
	rcvTime    time.Time
}

type PconnManager struct {
	pconns map[string]net.PacketConn //map the IP address to the UDP connection. may need a mutex to control it.

	Perspective   protocol.Perspective //record client or server
	LocalAddrs    []net.UDPAddr
	rcvRawPackets chan *receivedRawPacket //Put the raw data here
}

//Do the initial setup of PconnManager
func (pcm *PconnManager) Setup() error {
	utils.DefaultLogger.Debugf("setup the PconnManager")
	pcm.LocalAddrs = make([]net.UDPAddr, 0)
	pcm.pconns = make(map[string]net.PacketConn)

	go pcm.run()

	return nil
}

//The main loop of PconnManager, control multipath here.
func (pcm *PconnManager) run() {
	utils.DefaultLogger.Debugf("run the PconnManager")
	if pcm.Perspective == protocol.PerspectiveClient {
		pcm.CreateMultipath()
	}

}

func (pcm *PconnManager) CreateMultipath() error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}

	for _, interface_x := range ifaces {
		//hardcode here, need to do it more generally.
		if !strings.Contains(interface_x.Name, "enp0s3") && !strings.Contains(interface_x.Name, "enp0s8") && !strings.Contains(interface_x.Name, "eth") {
			continue
		}
		addrs, err := interface_x.Addrs()
		if err != nil {
			return err
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				return err
			}
			// If not Global Unicast, bypass
			if !ip.IsGlobalUnicast() {
				continue
			}
			found := false
		lookingLoop:
			for _, locAddr := range pcm.LocalAddrs {
				if ip.Equal(locAddr.IP) {
					found = true
					break lookingLoop
				}
			}
			if !found {
				locAddr, err := pcm.createPathConn(ip)
				if err != nil {
					return err
				}
				pcm.LocalAddrs = append(pcm.LocalAddrs, *locAddr)
			}
		}
	}

	utils.DebugNormolLog("local addrs:")
	utils.DebugNormolLog("%v", pcm.LocalAddrs)

	return nil
}

func (pcm *PconnManager) createPathConn(ip net.IP) (*net.UDPAddr, error) {
	pconn, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: 0})
	if err != nil {
		return nil, err
	}
	locAddr, err := net.ResolveUDPAddr("udp", pconn.LocalAddr().String())
	if err != nil {
		return nil, err
	}
	pcm.pconns[locAddr.String()] = pconn
	// Start to listen on this new socket
	go pcm.listen(pconn)
	return locAddr, nil
}

//receive the data on specific udp connection.
func (pcm *PconnManager) listen(pconn net.PacketConn) {
	var err error

listenLoop:
	for {
		var n int
		var addr net.Addr
		//Not using getPacketBuffer(), may take more time
		data := make([]byte, protocol.MaxPacketBufferSize)
		// The packet size should not exceed protocol.MaxReceivePacketSize bytes
		// If it does, we only read a truncate packet, which will then end up undecryptable
		n, addr, err = pconn.ReadFrom(data)
		if err != nil {
			break listenLoop
		}
		data = data[:n]
		rcvRawPacket := &receivedRawPacket{
			rcvPconn:   pconn,
			remoteAddr: addr,
			data:       data,
			rcvTime:    time.Now(),
		}
		pcm.rcvRawPackets <- rcvRawPacket
	}
}
