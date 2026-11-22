package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// labMode (TAK_GATEWAY_LAB=1) lets a single test host talk to itself:
// it keeps multicast loopback on and does not ignore our own source address.
var labMode = os.Getenv("TAK_GATEWAY_LAB") == "1"

// MeshGroup is one multicast group (SA or GeoChat) bridged on several interfaces.
type MeshGroup struct {
	Name   string
	Group  *net.UDPAddr
	Ifaces []string
	TTL    int

	mu       sync.Mutex
	rx       *net.UDPConn
	tx       map[string]*net.UDPConn // iface name -> sender
	nets     map[string][]*net.IPNet // iface name -> subnets
	ownIPs   map[string]bool
	joined   map[string]bool
	OnPacket func(iface string, src *net.UDPAddr, data []byte)
}

func NewMeshGroup(name, addr string, ifaces []string, ttl int) (*MeshGroup, error) {
	ga, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	if !ga.IP.IsMulticast() {
		return nil, fmt.Errorf("%s is not a multicast address", addr)
	}
	if ttl <= 0 {
		ttl = 1
	}
	return &MeshGroup{Name: name, Group: ga, Ifaces: ifaces, TTL: ttl,
		tx: map[string]*net.UDPConn{}, nets: map[string][]*net.IPNet{}, ownIPs: map[string]bool{}, joined: map[string]bool{}}, nil
}

func reuseControl(network, address string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return serr
}

// Run opens the sockets, keeps interface membership current and reads packets until ctx ends.
func (m *MeshGroup) Run(ctx context.Context) error {
	lc := net.ListenConfig{Control: reuseControl}
	pc, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", m.Group.Port))
	if err != nil {
		return fmt.Errorf("%s: listen: %w", m.Name, err)
	}
	m.rx = pc.(*net.UDPConn)
	_ = m.rx.SetReadBuffer(1 << 20)
	go func() {
		<-ctx.Done()
		m.rx.Close()
		m.mu.Lock()
		for _, c := range m.tx {
			c.Close()
		}
		m.mu.Unlock()
	}()
	m.refresh()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.refresh()
			}
		}
	}()
	buf := make([]byte, 65535)
	for {
		n, src, err := m.rx.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("%s: read: %v", m.Name, err)
			time.Sleep(time.Second)
			continue
		}
		m.mu.Lock()
		own := m.ownIPs[src.IP.String()] && !labMode
		iface := m.ifaceFor(src.IP)
		m.mu.Unlock()
		if own || m.OnPacket == nil {
			continue
		}
		m.OnPacket(iface, src, clone(buf[:n]))
	}
}

// refresh (re)joins the group on every configured interface that is up and has IPv4.
func (m *MeshGroup) refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	own := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				own[ipn.IP.String()] = true
			}
		}
	}
	m.ownIPs = own
	for _, name := range m.Ifaces {
		ifi, err := net.InterfaceByName(name)
		if err != nil || ifi.Flags&net.FlagUp == 0 {
			if m.joined[name] {
				log.Printf("%s: %s went away", m.Name, name)
			}
			m.dropIface(name)
			continue
		}
		var ip4 net.IP
		var nets []*net.IPNet
		if addrs, err := ifi.Addrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
					nets = append(nets, ipn)
					if ip4 == nil {
						ip4 = ipn.IP.To4()
					}
				}
			}
		}
		if ip4 == nil {
			m.dropIface(name)
			continue
		}
		m.nets[name] = nets
		if m.joined[name] {
			continue
		}
		if err := m.join(ifi); err != nil {
			log.Printf("%s: join %s on %s: %v", m.Name, m.Group, name, err)
			continue
		}
		tx, err := m.sender(ifi, ip4)
		if err != nil {
			log.Printf("%s: sender on %s: %v", m.Name, name, err)
			continue
		}
		m.tx[name] = tx
		m.joined[name] = true
		log.Printf("%s: bridging %s on %s (%s)", m.Name, m.Group, name, ip4)
	}
}

func (m *MeshGroup) dropIface(name string) {
	if c, ok := m.tx[name]; ok {
		c.Close()
		delete(m.tx, name)
	}
	delete(m.nets, name)
	if m.joined[name] {
		if ifi, err := net.InterfaceByName(name); err == nil {
			m.leave(ifi)
		}
	}
	delete(m.joined, name)
}

func mreq(group net.IP, ifi *net.Interface) *syscall.IPMreqn {
	r := &syscall.IPMreqn{Ifindex: int32(ifi.Index)}
	copy(r.Multiaddr[:], group.To4())
	return r
}

func (m *MeshGroup) join(ifi *net.Interface) error {
	rc, err := m.rx.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	rc.Control(func(fd uintptr) {
		serr = syscall.SetsockoptIPMreqn(int(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq(m.Group.IP, ifi))
		if serr == syscall.EADDRINUSE {
			serr = nil // already a member
		}
	})
	return serr
}

func (m *MeshGroup) leave(ifi *net.Interface) {
	if rc, err := m.rx.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			syscall.SetsockoptIPMreqn(int(fd), syscall.IPPROTO_IP, syscall.IP_DROP_MEMBERSHIP, mreq(m.Group.IP, ifi))
		})
	}
}

func (m *MeshGroup) sender(ifi *net.Interface, ip4 net.IP) (*net.UDPConn, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip4})
	if err != nil {
		return nil, err
	}
	rc, err := c.SyscallConn()
	if err != nil {
		c.Close()
		return nil, err
	}
	var serr error
	rc.Control(func(fd uintptr) {
		if serr = syscall.SetsockoptIPMreqn(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, &syscall.IPMreqn{Ifindex: int32(ifi.Index)}); serr != nil {
			return
		}
		if serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, m.TTL); serr != nil {
			return
		}
		loop := 0
		if labMode {
			loop = 1
		}
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, loop)
	})
	if serr != nil {
		c.Close()
		return nil, serr
	}
	return c, nil
}

// ifaceFor maps a source address to the bridged interface whose subnet contains it.
func (m *MeshGroup) ifaceFor(ip net.IP) string {
	for name, nets := range m.nets {
		for _, n := range nets {
			if n.Contains(ip) {
				return name
			}
		}
	}
	return ""
}

// Send multicasts data on every bridged interface except skip ("" = all).
func (m *MeshGroup) Send(data []byte, skip string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	sent := 0
	for name, c := range m.tx {
		if name == skip {
			continue
		}
		if _, err := c.WriteToUDP(data, m.Group); err != nil {
			log.Printf("%s: send on %s: %v", m.Name, name, err)
			continue
		}
		sent++
	}
	return sent
}

// Interfaces returns the interfaces currently bridged.
func (m *MeshGroup) Interfaces() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n := range m.tx {
		out = append(out, n)
	}
	return out
}
