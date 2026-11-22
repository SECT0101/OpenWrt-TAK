package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// ServerConfig describes the upstream TAK Server connection.
type ServerConfig struct {
	Host     string
	Port     int
	Proto    string // "ssl" or "tcp"
	Cert     string // client certificate (PEM)
	Key      string // client key (PEM)
	CA       string // server CA / truststore (PEM)
	Insecure bool   // skip server certificate verification
}

func (c ServerConfig) Addr() string { return net.JoinHostPort(c.Host, fmt.Sprint(c.Port)) }

func (c ServerConfig) tlsConfig() (*tls.Config, error) {
	tc := &tls.Config{ServerName: c.Host, InsecureSkipVerify: c.Insecure, MinVersion: tls.VersionTLS12}
	if c.Cert != "" || c.Key != "" {
		pair, err := tls.LoadX509KeyPair(c.Cert, c.Key)
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	if c.CA != "" {
		pem, err := os.ReadFile(c.CA)
		if err != nil {
			return nil, fmt.Errorf("CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file has no PEM certificates")
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

// Server keeps one connection to the TAK Server alive and moves events both ways.
type Server struct {
	Cfg     ServerConfig
	UID     string
	OnEvent func(raw []byte)
	OnUp    func()

	out chan []byte

	mu      sync.Mutex
	state   string
	lastErr string
	since   time.Time
}

func NewServer(cfg ServerConfig, uid string) *Server {
	return &Server{Cfg: cfg, UID: uid, out: make(chan []byte, 512), state: "connecting"}
}

// Send queues an event for the server; drops it if the link is down and the queue is full.
func (s *Server) Send(raw []byte) bool {
	select {
	case s.out <- raw:
		return true
	default:
		return false
	}
}

func (s *Server) set(state, err string) {
	s.mu.Lock()
	if s.state != state {
		s.since = time.Now()
	}
	s.state, s.lastErr = state, err
	s.mu.Unlock()
}

// Status returns state, last error and the time the state began.
func (s *Server) Status() (string, string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.lastErr, s.since
}

func (s *Server) dial(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if s.Cfg.Proto == "tcp" {
		return d.DialContext(ctx, "tcp", s.Cfg.Addr())
	}
	tc, err := s.Cfg.tlsConfig()
	if err != nil {
		return nil, err
	}
	td := &tls.Dialer{NetDialer: d, Config: tc}
	return td.DialContext(ctx, "tcp", s.Cfg.Addr())
}

// Run connects, reconnects with backoff, and pumps traffic until ctx ends.
func (s *Server) Run(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		s.set("connecting", "")
		conn, err := s.dial(ctx)
		if err != nil {
			s.set("down", err.Error())
			log.Printf("server %s: %v (retry in %s)", s.Cfg.Addr(), err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 2 * time.Second
		log.Printf("server %s: connected (%s)", s.Cfg.Addr(), s.Cfg.Proto)
		s.set("connected", "")
		err = s.session(ctx, conn)
		conn.Close()
		if ctx.Err() != nil {
			return
		}
		msg := "connection closed"
		if err != nil {
			msg = err.Error()
		}
		log.Printf("server %s: %s", s.Cfg.Addr(), msg)
		s.set("down", msg)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) session(ctx context.Context, conn net.Conn) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { // reader
		sp := &Splitter{}
		buf := make([]byte, 32*1024)
		for {
			conn.SetReadDeadline(time.Now().Add(3 * time.Minute))
			n, err := conn.Read(buf)
			if n > 0 {
				for _, ev := range sp.Feed(buf[:n]) {
					if s.OnEvent != nil {
						s.OnEvent(ev)
					}
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() { // writer + keep-alive
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		write := func(b []byte) error {
			conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
			_, err := conn.Write(b)
			return err
		}
		if s.OnUp != nil {
			s.OnUp()
		}
		for {
			select {
			case <-sctx.Done():
				errc <- nil
				return
			case b := <-s.out:
				if err := write(b); err != nil {
					errc <- err
					return
				}
			case <-ping.C:
				if err := write(BuildPing(s.UID, time.Now())); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	return <-errc
}
