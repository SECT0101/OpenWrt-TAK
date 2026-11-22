package main

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleEv = `<?xml version="1.0"?><event version="2.0" uid="ANDROID-1" type="a-f-G-U-C" how="m-g" time="2026-11-20T12:00:00.000Z" start="2026-11-20T12:00:00.000Z" stale="2026-11-20T12:05:00.000Z"><point lat="38.89" lon="-77.03" hae="200" ce="5" le="5"/><detail><contact callsign="ALPHA-1"/><__group name="Cyan" role="Team Member"/></detail></event>`

func TestSplitter(t *testing.T) {
	s := &Splitter{}
	stream := sampleEv + `<event uid="x" type="t-x-c-t" time="t"/>` + sampleEv
	var got [][]byte
	for i := 0; i < len(stream); i += 7 { // feed in awkward chunks
		end := i + 7
		if end > len(stream) {
			end = len(stream)
		}
		got = append(got, s.Feed([]byte(stream[i:end]))...)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	for _, g := range got {
		if !strings.HasPrefix(string(g), "<event") {
			t.Fatalf("bad split: %q", g)
		}
	}
}

func TestParseEvent(t *testing.T) {
	ev, err := ParseEvent([]byte(sampleEv))
	if err != nil {
		t.Fatal(err)
	}
	if ev.UID != "ANDROID-1" || ev.Type != "a-f-G-U-C" || ev.Callsign != "ALPHA-1" || !ev.HasPoint || ev.Lat != 38.89 || ev.Lon != -77.03 {
		t.Fatalf("parsed %+v", ev)
	}
	if _, err := ParseEvent([]byte("<foo/>")); err == nil {
		t.Fatal("expected error for non-event")
	}
}

func TestTakProtoV1(t *testing.T) {
	contact := append(pbStr(1, "192.168.1.5:4242:tcp"), pbStr(2, "PINE-1")...)
	group := append(pbStr(1, "Cyan"), pbStr(2, "Team Lead")...)
	takv := append(append(pbStr(1, "Pixel"), pbStr(2, "ATAK-CIV")...), append(pbStr(3, "34"), pbStr(4, "5.8")...)...)
	track := append(pbDbl(1, 1.5), pbDbl(2, 90)...)
	detail := append(append(append(pbStr(1, `<uid Droid="PINE-1"/>`), pbBytes(2, contact)...), pbBytes(3, group)...), append(pbBytes(6, takv), pbBytes(7, track)...)...)
	ms := uint64(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).UnixMilli())
	ev := append(pbStr(1, "a-f-G-U-C"), pbStr(5, "ANDROID-abc")...)
	ev = append(ev, pbVarint(6, ms)...)
	ev = append(ev, pbVarint(7, ms)...)
	ev = append(ev, pbVarint(8, ms+300000)...)
	ev = append(ev, pbStr(9, "m-g")...)
	ev = append(ev, pbDbl(10, 38.9)...)
	ev = append(ev, pbDbl(11, -77.0)...)
	ev = append(ev, pbDbl(12, 180)...)
	ev = append(ev, pbDbl(13, 4)...)
	ev = append(ev, pbDbl(14, 9999999)...)
	ev = append(ev, pbBytes(15, detail)...)
	pkt := append([]byte{0xbf, 0x01, 0xbf}, pbBytes(2, ev)...)
	x, err := TakProtoToXML(pkt)
	if err != nil {
		t.Fatal(err)
	}
	e, err := ParseEvent(x)
	if err != nil {
		t.Fatalf("%v\n%s", err, x)
	}
	if e.UID != "ANDROID-abc" || e.Callsign != "PINE-1" || e.Lat != 38.9 || e.Time != "2026-11-20T12:00:00.000Z" {
		t.Fatalf("got %+v\n%s", e, x)
	}
	for _, want := range []string{`<__group name="Cyan" role="Team Lead"/>`, `<takv device="Pixel"`, `<track speed="1.50" course="90.00"/>`, `<uid Droid="PINE-1"/>`} {
		if !strings.Contains(string(x), want) {
			t.Fatalf("missing %s in %s", want, x)
		}
	}
	// control message (takControl only) must be rejected, not crash
	if _, err := TakProtoToXML(append([]byte{0xbf, 0x01, 0xbf}, pbBytes(1, pbVarint(1, 1))...)); err == nil {
		t.Fatal("control message should not produce an event")
	}
	if _, err := TakProtoToXML([]byte{0xbf, 0x01, 0xbf, 0xff, 0xff}); err == nil {
		t.Fatal("garbage should fail")
	}
}

func TestBuildSelf(t *testing.T) {
	b := BuildSelf(SelfInfo{UID: "gw-1", Callsign: `SHOP <ONE> & "two"`, Type: "a-f-G-U-C", Team: "Cyan", Role: "HQ", Lat: 38.9, Lon: -77.0, CE: 5}, time.Now())
	e, err := ParseEvent(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if e.Callsign != `SHOP <ONE> & "two"` || e.UID != "gw-1" {
		t.Fatalf("got %+v", e)
	}
	if p, _ := ParseEvent(BuildPing("gw-1", time.Now())); p == nil || !p.IsPing() {
		t.Fatal("ping not recognized")
	}
}

func TestImportDataPackage(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("openssl", args...).CombinedOutput(); err != nil {
			t.Fatalf("openssl %v: %v %s", args, err, out)
		}
	}
	run("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", dir+"/ca.key", "-out", dir+"/ca.crt", "-subj", "/CN=TAK CA", "-days", "30")
	run("req", "-newkey", "rsa:2048", "-nodes", "-keyout", dir+"/u.key", "-out", dir+"/u.csr", "-subj", "/CN=sect-one")
	run("x509", "-req", "-in", dir+"/u.csr", "-CA", dir+"/ca.crt", "-CAkey", dir+"/ca.key", "-CAcreateserial", "-out", dir+"/u.crt", "-days", "30")
	run("pkcs12", "-export", "-in", dir+"/u.crt", "-inkey", dir+"/u.key", "-out", dir+"/sect-one.p12", "-passout", "pass:atakatak")
	run("pkcs12", "-export", "-nokeys", "-in", dir+"/ca.crt", "-out", dir+"/truststore-root.p12", "-passout", "pass:atakatak")
	pref := `<?xml version='1.0' standalone='yes'?><preferences><preference version="1" name="cot_streams"><entry key="count" class="class java.lang.Integer">1</entry><entry key="description0" class="class java.lang.String">SECT TAK</entry><entry key="enabled0" class="class java.lang.Boolean">true</entry><entry key="connectString0" class="class java.lang.String">tak.example.net:8089:ssl</entry></preference><preference version="1" name="com.atakmap.app_preferences"><entry key="caLocation" class="class java.lang.String">cert/truststore-root.p12</entry><entry key="caPassword" class="class java.lang.String">atakatak</entry><entry key="clientPassword" class="class java.lang.String">atakatak</entry><entry key="certificateLocation" class="class java.lang.String">cert/sect-one.p12</entry></preference></preferences>`
	zf, _ := os.Create(filepath.Join(dir, "dp.zip"))
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("abc123/server.pref")
	w.Write([]byte(pref))
	for _, n := range []string{"sect-one.p12", "truststore-root.p12"} {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		w, _ := zw.Create("abc123/" + n)
		w.Write(b)
	}
	zw.Close()
	zf.Close()
	out := filepath.Join(dir, "out")
	res := doImport(filepath.Join(dir, "dp.zip"), out, "")
	if !res.OK {
		t.Fatalf("import failed: %+v", res)
	}
	if res.Host != "tak.example.net" || res.Port != "8089" || res.Protocol != "ssl" || res.Description != "SECT TAK" {
		t.Fatalf("got %+v", res)
	}
	cfg := ServerConfig{Host: res.Host, Cert: res.Cert, Key: res.Key, CA: res.CA}
	if _, err := cfg.tlsConfig(); err != nil {
		t.Fatalf("imported PEMs unusable: %v", err)
	}
}
