package strategy

import (
	"bytes"
	"hash/fnv"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nadoo/glider/common/log"
	"github.com/nadoo/glider/common/timewindow"
	"github.com/nadoo/glider/proxy"
)

// Config is strategy config struct.
type Config struct {
	Strategy          string
	CheckWebSite      string
	CheckInterval     int
	CheckTimeout      int
	CheckDisabledOnly bool
	MaxFailures       int
	DialTimeout       int
	RelayTimeout      int
	IntFace           string
	ForwardTime       []timewindow.TimeWindow
	RejectTime        []timewindow.TimeWindow
}

// forwarder slice orderd by priority
type priSlice []*Forwarder

func (p priSlice) Len() int           { return len(p) }
func (p priSlice) Less(i, j int) bool { return p[i].Priority() > p[j].Priority() }
func (p priSlice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

// Proxy is base proxy struct.
type Proxy struct {
	name       string
	config     *Config
	fwdrs      priSlice
	avail      []*Forwarder // available forwarders
	rejectFwdr *Forwarder
	mu         sync.RWMutex
	index      uint32
	priority   uint32
	next       func(addr string) *Forwarder
}

// NewProxy returns a new strategy proxy.
func NewProxy(name string, s []string, c *Config) *Proxy {
	var fwdrs []*Forwarder
	for _, chain := range s {
		fwdr, err := ForwarderFromURL(chain, c.IntFace,
			time.Duration(c.DialTimeout)*time.Second, time.Duration(c.RelayTimeout)*time.Second)
		if err != nil {
			log.Fatal(err)
		}
		fwdr.SetMaxFailures(uint32(c.MaxFailures))
		fwdrs = append(fwdrs, fwdr)
	}

	if len(fwdrs) == 0 {
		// direct forwarder
		fwdrs = append(fwdrs, DirectForwarder(c.IntFace,
			time.Duration(c.DialTimeout)*time.Second, time.Duration(c.RelayTimeout)*time.Second))
		c.Strategy = "rr"
	}

	return newProxy(name, fwdrs, c)
}

// newProxy returns a new Proxy.
func newProxy(name string, fwdrs []*Forwarder, c *Config) *Proxy {
	log.F("strategy.newProxy: " + name)
	rejectFwdr, err := ForwarderFromURL("reject://", "", 0, 0)
	if err != nil {
		log.Fatal(err)
	}

	p := &Proxy{name: name, fwdrs: fwdrs, config: c, rejectFwdr: rejectFwdr}
	sort.Sort(p.fwdrs)

	p.init()

	if strings.IndexByte(p.config.CheckWebSite, ':') == -1 {
		p.config.CheckWebSite += ":80"
	}

	switch c.Strategy {
	case "rr":
		p.next = p.scheduleRR
		log.F("[strategy] %s: forward in round robin mode.", name)
	case "ha":
		p.next = p.scheduleHA
		log.F("[strategy] %s: forward in high availability mode.", name)
	case "lha":
		p.next = p.scheduleLHA
		log.F("[strategy] %s: forward in latency based high availability mode.", name)
	case "dh":
		p.next = p.scheduleDH
		log.F("[strategy] %s: forward in destination hashing mode.", name)
	default:
		p.next = p.scheduleRR
		log.F("[strategy] %s: not supported forward mode '%s', use round robin mode.", name, c.Strategy)
	}

	for _, f := range fwdrs {
		f.AddHandler(p.onStatusChanged)
	}

	return p
}

// Dial connects to the address addr on the network net.
func (p *Proxy) Dial(network, addr string) (net.Conn, proxy.Dialer, error) {
	nd := p.NextDialer(addr)
	c, err := nd.Dial(network, addr)
	return c, nd, err
}

// DialUDP connects to the given address.
func (p *Proxy) DialUDP(network, addr string) (pc net.PacketConn, writeTo net.Addr, err error) {
	return p.NextDialer(addr).DialUDP(network, addr)
}

// NextDialer returns the next dialer.
func (p *Proxy) NextDialer(dstAddr string) proxy.Dialer {
	allowed := false
	now := time.Now()

	if len(p.config.ForwardTime) == 0 {
		// Default is to allow
		allowed = true
	} else {
		for _, forwardTime := range p.config.ForwardTime {
			if forwardTime.Contains(now) {
				allowed = true
				break
			}
		}
	}

	if !allowed {
		log.F("[%s] NOT ALLOWED [%s]", p.name, dstAddr)
	} else {
		for _, rejectTime := range p.config.RejectTime {
			if rejectTime.Contains(now) {
				log.F("[%s] REJECTED [%s] [%s]", p.name, rejectTime.String(), dstAddr)
				allowed = false
				break
			}
		}
	}

	if !allowed {
		return p.rejectFwdr
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.avail) == 0 {
		return p.fwdrs[atomic.AddUint32(&p.index, 1)%uint32(len(p.fwdrs))]
	}

	return p.next(dstAddr)
}

// Record records result while using the dialer from proxy.
func (p *Proxy) Record(dialer proxy.Dialer, success bool) {
	OnRecord(dialer, success)
}

// OnRecord records result while using the dialer from proxy.
func OnRecord(dialer proxy.Dialer, success bool) {
	if fwdr, ok := dialer.(*Forwarder); ok {
		if !success {
			fwdr.IncFailures()
		} else {
			fwdr.Enable()
		}
	}
}

// Priority returns the active priority of dialer.
func (p *Proxy) Priority() uint32 { return atomic.LoadUint32(&p.priority) }

// SetPriority sets the active priority of daler.
func (p *Proxy) SetPriority(pri uint32) { atomic.StoreUint32(&p.priority, pri) }

// init traverse d.fwdrs and init the available forwarder slice.
func (p *Proxy) init() {
	for _, f := range p.fwdrs {
		if f.Enabled() {
			p.SetPriority(f.Priority())
			break
		}
	}

	p.avail = nil
	for _, f := range p.fwdrs {
		if f.Enabled() && f.Priority() >= p.Priority() {
			p.avail = append(p.avail, f)
		}
	}

	if len(p.avail) == 0 {
		// no available forwarders, set priority to 0 to check all forwarders in check func
		p.SetPriority(0)
		// log.F("[strategy] no available forwarders, please check your config file or network settings")
	}
}

// onStatusChanged will be called when fwdr's status changed.
func (p *Proxy) onStatusChanged(fwdr *Forwarder) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if fwdr.Enabled() {
		log.F("[strategy] %s changed status from Disabled to Enabled ", fwdr.Addr())
		if fwdr.Priority() == p.Priority() {
			p.avail = append(p.avail, fwdr)
		} else if fwdr.Priority() > p.Priority() {
			p.init()
		}
	} else {
		log.F("[strategy] %s changed status from Enabled to Disabled", fwdr.Addr())
		for i, f := range p.avail {
			if f == fwdr {
				p.avail[i], p.avail = p.avail[len(p.avail)-1], p.avail[:len(p.avail)-1]
				break
			}
		}
	}

	if len(p.avail) == 0 {
		p.init()
	}
}

// Check implements the Checker interface.
func (p *Proxy) Check() {
	// no need to check when there's only 1 forwarder
	if len(p.fwdrs) > 1 {
		for i := 0; i < len(p.fwdrs); i++ {
			go p.check(p.fwdrs[i])
		}
	}
}

func (p *Proxy) check(f *Forwarder) {
	wait := uint8(0)
	buf := make([]byte, 4)
	intval := time.Duration(p.config.CheckInterval) * time.Second

	for {
		time.Sleep(intval * time.Duration(wait))

		// check all forwarders at least one time
		if wait > 0 && (f.Priority() < p.Priority()) {
			continue
		}

		if f.Enabled() && p.config.CheckDisabledOnly {
			continue
		}

		if checkWebSite(f, p.config.CheckWebSite, time.Duration(p.config.CheckTimeout)*time.Second, buf) {
			wait = 1
			continue
		}

		if wait == 0 {
			wait = 1
		}

		wait *= 2
		if wait > 16 {
			wait = 16
		}
	}
}

func checkWebSite(fwdr *Forwarder, website string, timeout time.Duration, buf []byte) bool {
	startTime := time.Now()

	rc, err := fwdr.Dial("tcp", website)
	if err != nil {
		fwdr.Disable()
		log.F("[check] %s(%d) -> %s, DISABLED. error in dial: %s", fwdr.Addr(), fwdr.Priority(),
			website, err)
		return false
	}
	defer rc.Close()

	if timeout > 0 {
		rc.SetDeadline(time.Now().Add(timeout))
	}

	_, err = io.WriteString(rc, "GET / HTTP/1.0\r\n\r\n")
	if err != nil {
		fwdr.Disable()
		log.F("[check] %s(%d) -> %s, DISABLED. error in write: %s", fwdr.Addr(), fwdr.Priority(),
			website, err)
		return false
	}

	_, err = io.ReadFull(rc, buf)
	if err != nil {
		fwdr.Disable()
		log.F("[check] %s(%d) -> %s, DISABLED. error in read: %s", fwdr.Addr(), fwdr.Priority(),
			website, err)
		return false
	}

	if !bytes.Equal([]byte("HTTP"), buf) {
		fwdr.Disable()
		log.F("[check] %s(%d) -> %s, DISABLED. server response: %s", fwdr.Addr(), fwdr.Priority(),
			website, buf)
		return false
	}

	readTime := time.Since(startTime)
	fwdr.SetLatency(int64(readTime))

	if readTime > timeout {
		fwdr.Disable()
		log.F("[check] %s(%d) -> %s, DISABLED. check timeout: %s", fwdr.Addr(), fwdr.Priority(),
			website, readTime)
		return false
	}

	fwdr.Enable()
	log.F("[check] %s(%d) -> %s, ENABLED. connect time: %s", fwdr.Addr(), fwdr.Priority(),
		website, readTime)

	return true
}

// Round Robin
func (p *Proxy) scheduleRR(dstAddr string) *Forwarder {
	return p.avail[atomic.AddUint32(&p.index, 1)%uint32(len(p.avail))]
}

// High Availability
func (p *Proxy) scheduleHA(dstAddr string) *Forwarder {
	return p.avail[0]
}

// Latency based High Availability
func (p *Proxy) scheduleLHA(dstAddr string) *Forwarder {
	fwdr := p.avail[0]
	lowest := fwdr.Latency()
	for _, f := range p.avail {
		if f.Latency() < lowest {
			lowest = f.Latency()
			fwdr = f
		}
	}
	return fwdr
}

// Destination Hashing
func (p *Proxy) scheduleDH(dstAddr string) *Forwarder {
	fnv1a := fnv.New32a()
	fnv1a.Write([]byte(dstAddr))
	return p.avail[fnv1a.Sum32()%uint32(len(p.avail))]
}
