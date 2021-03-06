package ss

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nadoo/go-shadowsocks2/core"

	"github.com/nadoo/glider/common/conn"
	"github.com/nadoo/glider/common/log"
	"github.com/nadoo/glider/common/pool"
	"github.com/nadoo/glider/common/socks"
	"github.com/nadoo/glider/proxy"
)

// SS is a base ss struct.
type SS struct {
	dialer proxy.Dialer
	proxy  proxy.Proxy
	addr   string

	core.Cipher
}

func init() {
	proxy.RegisterDialer("ss", NewSSDialer)
	proxy.RegisterServer("ss", NewSSServer)
}

// NewSS returns a ss proxy.
func NewSS(s string, d proxy.Dialer, p proxy.Proxy) (*SS, error) {
	u, err := url.Parse(s)
	if err != nil {
		log.F("parse err: %s", err)
		return nil, err
	}

	addr := u.Host
	method := u.User.Username()
	pass, _ := u.User.Password()

	ciph, err := core.PickCipher(method, nil, pass)
	if err != nil {
		log.Fatalf("[ss] PickCipher for '%s', error: %s", method, err)
	}

	ss := &SS{
		dialer: d,
		proxy:  p,
		addr:   addr,
		Cipher: ciph,
	}

	return ss, nil
}

// NewSSDialer returns a ss proxy dialer.
func NewSSDialer(s string, d proxy.Dialer) (proxy.Dialer, error) {
	return NewSS(s, d, nil)
}

// NewSSServer returns a ss proxy server.
func NewSSServer(s string, p proxy.Proxy) (proxy.Server, error) {
	return NewSS(s, nil, p)
}

// ListenAndServe serves ss requests.
func (s *SS) ListenAndServe() {
	go s.ListenAndServeUDP()
	s.ListenAndServeTCP()
}

// ListenAndServeTCP serves tcp ss requests.
func (s *SS) ListenAndServeTCP() {
	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.F("[ss] failed to listen on %s: %v", s.addr, err)
		return
	}

	log.F("[ss] listening TCP on %s", s.addr)

	for {
		c, err := l.Accept()
		if err != nil {
			log.F("[ss] failed to accept: %v", err)
			continue
		}
		go s.Serve(c)
	}

}

// Serve serves a connection.
func (s *SS) Serve(c net.Conn) {
	defer c.Close()

	if c, ok := c.(*net.TCPConn); ok {
		c.SetKeepAlive(true)
	}

	c = s.StreamConn(c)

	tgt, err := socks.ReadAddr(c)
	if err != nil {
		log.F("[ss] failed to get target address: %v", err)
		return
	}

	dialer := s.proxy.NextDialer(tgt.String())

	// udp over tcp?
	uot := socks.UoT(tgt[0])
	if uot && dialer.Addr() == "DIRECT" {
		rc, err := net.ListenPacket("udp", "")
		if err != nil {
			log.F("[ss-uottun] UDP remote listen error: %v", err)
		}
		defer rc.Close()

		buf := pool.GetBuffer(conn.UDPBufSize)
		defer pool.PutBuffer(buf)

		n, err := c.Read(buf)
		if err != nil {
			log.F("[ss-uottun] error in read: %s\n", err)
			return
		}

		tgtAddr, _ := net.ResolveUDPAddr("udp", tgt.String())
		rc.WriteTo(buf[:n], tgtAddr)

		n, _, err = rc.ReadFrom(buf)
		if err != nil {
			log.F("[ss-uottun] read error: %v", err)
		}

		c.Write(buf[:n])

		log.F("[ss] %s <-tcp-> %s - %s <-udp-> %s ", c.RemoteAddr(), c.LocalAddr(), rc.LocalAddr(), tgt)

		return
	}

	network := "tcp"
	if uot {
		network = "udp"
	}

	rc, err := dialer.Dial(network, tgt.String())
	if err != nil {
		log.F("[ss] %s <-> %s via %s, error in dial: %v", c.RemoteAddr(), tgt, dialer.Addr(), err)
		return
	}
	defer rc.Close()

	log.F("[ss] %s <-> %s via %s", c.RemoteAddr(), tgt, dialer.Addr())

	if err = conn.Relay(c, rc); err != nil {
		log.F("[ss] relay error: %v", err)
		s.proxy.Record(dialer, false)
	}
}

// ListenAndServeUDP serves udp ss requests.
func (s *SS) ListenAndServeUDP() {
	lc, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		log.F("[ss-udp] failed to listen on %s: %v", s.addr, err)
		return
	}
	defer lc.Close()

	lc = s.PacketConn(lc)

	log.F("[ss-udp] listening UDP on %s", s.addr)

	var nm sync.Map
	buf := make([]byte, conn.UDPBufSize)

	for {
		c := NewPktConn(lc, nil, nil, true)

		n, raddr, err := c.ReadFrom(buf)
		if err != nil {
			log.F("[ss-udp] remote read error: %v", err)
			continue
		}

		var pc *PktConn
		v, ok := nm.Load(raddr.String())
		if !ok && v == nil {
			lpc, nextHop, err := s.proxy.DialUDP("udp", c.tgtAddr.String())
			if err != nil {
				log.F("[ss-udp] remote dial error: %v", err)
				continue
			}

			pc = NewPktConn(lpc, nextHop, nil, false)
			nm.Store(raddr.String(), pc)

			go func() {
				conn.RelayUDP(c, raddr, pc, 2*time.Minute)
				pc.Close()
				nm.Delete(raddr.String())
			}()

			log.F("[ss-udp] %s <-> %s", raddr, c.tgtAddr)

		} else {
			pc = v.(*PktConn)
		}

		_, err = pc.WriteTo(buf[:n], pc.writeAddr)
		if err != nil {
			log.F("[ss-udp] remote write error: %v", err)
			continue
		}

		// log.F("[ss-udp] %s <-> %s", raddr, c.tgtAddr)
	}
}

// ListCipher returns all the ciphers supported.
func ListCipher() string {
	return strings.Join(core.ListCipher(), " ")
}

// Addr returns forwarder's address.
func (s *SS) Addr() string {
	if s.addr == "" {
		return s.dialer.Addr()
	}
	return s.addr
}

// Dial connects to the address addr on the network net via the proxy.
func (s *SS) Dial(network, addr string) (net.Conn, error) {
	target := socks.ParseAddr(addr)
	if target == nil {
		return nil, errors.New("[ss] unable to parse address: " + addr)
	}

	if network == "uot" {
		target[0] = target[0] | 0x8
	}

	c, err := s.dialer.Dial("tcp", s.addr)
	if err != nil {
		log.F("[ss] dial to %s error: %s", s.addr, err)
		return nil, err
	}

	c = s.StreamConn(c)
	if _, err = c.Write(target); err != nil {
		c.Close()
		return nil, err
	}

	return c, err

}

// DialUDP connects to the given address via the proxy.
func (s *SS) DialUDP(network, addr string) (net.PacketConn, net.Addr, error) {
	pc, nextHop, err := s.dialer.DialUDP(network, s.addr)
	if err != nil {
		log.F("[ss] dialudp to %s error: %s", s.addr, err)
		return nil, nil, err
	}

	pkc := NewPktConn(s.PacketConn(pc), nextHop, socks.ParseAddr(addr), true)
	return pkc, nextHop, err
}
