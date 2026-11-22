package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestEndToEnd runs a fake TLS TAK Server and real multicast on a host interface:
// ATAK-style SA on the mesh must reach the server, server events must reach the mesh,
// and the gateway must beacon its own position to both.
func TestEndToEnd(t *testing.T) {
	ifn := os.Getenv("TAK_TEST_IFACE")
	if ifn == "" || !labMode {
		t.Skip("set TAK_GATEWAY_LAB=1 TAK_TEST_IFACE=<multicast iface>")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("openssl", args...).CombinedOutput(); err != nil {
			t.Fatalf("openssl %v: %v %s", args, err, out)
		}
	}
	// CA, server cert (CN/SAN 127.0.0.1), client cert
	run("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", dir+"/ca.key", "-out", dir+"/ca.pem", "-subj", "/CN=Test TAK CA", "-days", "2")
	run("req", "-newkey", "rsa:2048", "-nodes", "-keyout", dir+"/srv.key", "-out", dir+"/srv.csr", "-subj", "/CN=127.0.0.1")
	os.WriteFile(dir+"/san.ext", []byte("subjectAltName=IP:127.0.0.1\n"), 0644)
	run("x509", "-req", "-in", dir+"/srv.csr", "-CA", dir+"/ca.pem", "-CAkey", dir+"/ca.key", "-CAcreateserial", "-out", dir+"/srv.pem", "-days", "2", "-extfile", dir+"/san.ext")
	run("req", "-newkey", "rsa:2048", "-nodes", "-keyout", dir+"/cli.key", "-out", dir+"/cli.csr", "-subj", "/CN=gateway")
	run("x509", "-req", "-in", dir+"/cli.csr", "-CA", dir+"/ca.pem", "-CAkey", dir+"/ca.key", "-CAcreateserial", "-out", dir+"/cli.pem", "-days", "2")

	// fake TAK server requiring a client certificate
	srvPair, _ := tls.LoadX509KeyPair(dir+"/srv.pem", dir+"/srv.key")
	caPEM, _ := os.ReadFile(dir + "/ca.pem")
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{srvPair}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var got []string
	var conn net.Conn
	connected := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		conn = c
		mu.Unlock()
		close(connected)
		sp := &Splitter{}
		buf := make([]byte, 65536)
		for {
			n, err := c.Read(buf)
			for _, ev := range sp.Feed(buf[:n]) {
				mu.Lock()
				got = append(got, string(ev))
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	gw := &Gateway{
		Self:       SelfInfo{UID: "tak-gateway-test", Callsign: "SHOP-ONE", Type: "a-f-G-U-C", Team: "Cyan", Role: "HQ", Version: "test"},
		Loc:        &Locator{Static: Position{Lat: 38.9, Lon: -77.0, Source: "static"}},
		Beacon:     5 * time.Second,
		ToServer:   true,
		FromServer: true,
		StatusPath: dir + "/status.json",
		Server:     NewServer(ServerConfig{Host: "127.0.0.1", Port: port, Proto: "ssl", Cert: dir + "/cli.pem", Key: dir + "/cli.key", CA: dir + "/ca.pem"}, "tak-gateway-test"),
	}
	gw.SA, _ = NewMeshGroup("sa", "239.2.3.1:6969", []string{ifn}, 1)
	gw.Chat, _ = NewMeshGroup("chat", "224.10.10.1:17012", []string{ifn}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.Run(ctx)

	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("gateway never connected to the TLS server")
	}

	// a listener standing in for an ATAK phone on the mesh
	ifi, _ := net.InterfaceByName(ifn)
	phone, err := net.ListenMulticastUDP("udp4", ifi, &net.UDPAddr{IP: net.IPv4(239, 2, 3, 1), Port: 6969})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	time.Sleep(1500 * time.Millisecond) // let the gateway join

	// 1) phone multicasts SA -> server receives it
	tx, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(239, 2, 3, 1), Port: 6969})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(cotTime)
	phoneEv := `<event version="2.0" uid="ANDROID-phone1" type="a-f-G-U-C" how="m-g" time="` + now + `" start="` + now + `" stale="` + now + `"><point lat="39.0" lon="-77.1" hae="0" ce="5" le="5"/><detail><contact callsign="RANGER-1"/></detail></event>`
	tx.Write([]byte(phoneEv))
	tx.Write([]byte(phoneEv)) // duplicate must be dropped

	// 2) server sends an event -> it shows up on the mesh
	srvEv := `<event version="2.0" uid="SERVER-UNIT-9" type="a-h-G" how="h-e" time="` + now + `" start="` + now + `" stale="` + now + `"><point lat="38.8" lon="-76.9" hae="0" ce="9" le="9"/><detail><contact callsign="HOSTILE-9"/></detail></event>`
	mu.Lock()
	conn.Write([]byte(srvEv))
	mu.Unlock()

	deadline := time.Now().Add(8 * time.Second)
	sawServerEvOnMesh, sawBeaconOnMesh := false, false
	buf := make([]byte, 65536)
	for time.Now().Before(deadline) && !(sawServerEvOnMesh && sawBeaconOnMesh) {
		phone.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := phone.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		s := string(buf[:n])
		if strings.Contains(s, "SERVER-UNIT-9") {
			sawServerEvOnMesh = true
		}
		if strings.Contains(s, `uid="tak-gateway-test"`) {
			sawBeaconOnMesh = true
		}
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	all := strings.Join(got, "\n")
	phoneCount := strings.Count(all, "ANDROID-phone1")
	mu.Unlock()
	if phoneCount != 1 {
		t.Errorf("server should get the phone's SA exactly once, got %d", phoneCount)
	}
	if !strings.Contains(all, `uid="tak-gateway-test"`) {
		t.Errorf("server never got the gateway beacon")
	}
	if !sawServerEvOnMesh {
		t.Errorf("server event never reached the mesh")
	}
	if !sawBeaconOnMesh {
		t.Errorf("gateway beacon never reached the mesh")
	}
	gw.writeStatus()
	st, _ := os.ReadFile(dir + "/status.json")
	if !strings.Contains(string(st), `"state": "connected"`) || !strings.Contains(string(st), "RANGER-1") {
		t.Errorf("status file incomplete:\n%s", st)
	}
	t.Logf("status:\n%s", st)
}
