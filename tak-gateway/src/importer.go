package main

// "tak-gateway import" turns a TAK Server connection data package (.zip with a
// .pref file and .p12 certificates) into PEM files plus the server address.
// .p12 decoding uses the openssl command (package openssl-util on OpenWrt).

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

type ImportResult struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Description string `json:"description,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        string `json:"port,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Cert        string `json:"cert,omitempty"`
	Key         string `json:"key,omitempty"`
	CA          string `json:"ca,omitempty"`
	Note        string `json:"note,omitempty"`
}

type prefFile struct {
	Prefs []struct {
		Name    string `xml:"name,attr"`
		Entries []struct {
			Key   string `xml:"key,attr"`
			Value string `xml:",chardata"`
		} `xml:"entry"`
	} `xml:"preference"`
}

func parsePref(b []byte) map[string]string {
	var p prefFile
	out := map[string]string{}
	if xml.Unmarshal(b, &p) != nil {
		return out
	}
	for _, pr := range p.Prefs {
		for _, e := range pr.Entries {
			out[e.Key] = strings.TrimSpace(e.Value)
		}
	}
	return out
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

func runOpenSSL(args ...string) error {
	if _, err := exec.LookPath("openssl"); err != nil {
		return errors.New("the openssl command is needed to read .p12 files (install openssl-util)")
	}
	try := func(a []string) error {
		out, err := exec.Command("openssl", a...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	err := try(args)
	if err != nil { // OpenSSL 3 needs -legacy for older TAK p12 files
		if err2 := try(append([]string{args[0], "-legacy"}, args[1:]...)); err2 == nil {
			return nil
		}
	}
	return err
}

// p12ToPEM writes cert/key (client) or CA bundle (truststore) PEM files.
func p12ToPEM(p12, pass, dir, base string, client bool) (cert, key string, err error) {
	pin := "pass:" + pass
	if client {
		cert = filepath.Join(dir, base+".crt.pem")
		key = filepath.Join(dir, base+".key.pem")
		if err = runOpenSSL("pkcs12", "-in", p12, "-clcerts", "-nokeys", "-passin", pin, "-out", cert); err != nil {
			return
		}
		if err = runOpenSSL("pkcs12", "-in", p12, "-nocerts", "-nodes", "-passin", pin, "-out", key); err != nil {
			return
		}
		os.Chmod(key, 0600)
		return
	}
	cert = filepath.Join(dir, base+".pem")
	err = runOpenSSL("pkcs12", "-in", p12, "-nokeys", "-passin", pin, "-out", cert)
	return
}

func cmdImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dir := fs.String("dir", "/etc/tak-gateway", "where to write the PEM files")
	pass := fs.String("password", "", "certificate password (overrides the one in the package)")
	fs.Parse(args)
	res := doImport(fs.Arg(0), *dir, *pass)
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	if !res.OK {
		return 1
	}
	return 0
}

func doImport(file, dir, pass string) ImportResult {
	if file == "" {
		return ImportResult{Error: "usage: tak-gateway import [-dir DIR] [-password PW] <data-package.zip | client.p12>"}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ImportResult{Error: err.Error()}
	}
	if strings.HasSuffix(strings.ToLower(file), ".p12") {
		c, k, err := p12ToPEM(file, pass, dir, "client", true)
		if err != nil {
			return ImportResult{Error: err.Error()}
		}
		return ImportResult{OK: true, Cert: c, Key: k, Note: "client certificate only; set the server address and CA yourself"}
	}
	zr, err := zip.OpenReader(file)
	if err != nil {
		return ImportResult{Error: "not a data package: " + err.Error()}
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	var prefs []byte
	for _, f := range zr.File {
		files[strings.ToLower(path.Base(f.Name))] = f
		if strings.HasSuffix(strings.ToLower(f.Name), ".pref") {
			rc, err := f.Open()
			if err == nil {
				prefs, _ = io.ReadAll(rc)
				rc.Close()
			}
		}
	}
	if prefs == nil {
		return ImportResult{Error: "no .pref file in the data package"}
	}
	p := parsePref(prefs)
	res := ImportResult{Description: firstOf(p, "description0")}
	cs := firstOf(p, "connectString0")
	if parts := strings.Split(cs, ":"); len(parts) == 3 {
		res.Host, res.Port, res.Protocol = parts[0], parts[1], parts[2]
		if res.Protocol == "quic" {
			res.Note = "QUIC streams are not supported; using SSL on the same host"
			res.Protocol = "ssl"
		}
	} else {
		return ImportResult{Error: "no connectString0 in the .pref file"}
	}
	extract := func(loc string) (string, error) {
		f := files[strings.ToLower(path.Base(loc))]
		if f == nil {
			return "", fmt.Errorf("%s is not in the package", path.Base(loc))
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		out := filepath.Join(dir, path.Base(loc))
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		defer w.Close()
		_, err = io.Copy(w, rc)
		return out, err
	}
	if ca := firstOf(p, "caLocation0", "caLocation"); ca != "" {
		f, err := extract(ca)
		if err != nil {
			return ImportResult{Error: "CA: " + err.Error()}
		}
		pw := pass
		if pw == "" {
			pw = firstOf(p, "caPassword0", "caPassword")
		}
		if res.CA, _, err = p12ToPEM(f, pw, dir, "ca", false); err != nil {
			return ImportResult{Error: "CA: " + err.Error()}
		}
	}
	if cl := firstOf(p, "certificateLocation0", "certificateLocation"); cl != "" {
		f, err := extract(cl)
		if err != nil {
			return ImportResult{Error: "client certificate: " + err.Error()}
		}
		pw := pass
		if pw == "" {
			pw = firstOf(p, "clientPassword0", "clientPassword")
		}
		if res.Cert, res.Key, err = p12ToPEM(f, pw, dir, "client", true); err != nil {
			return ImportResult{Error: "client certificate: " + err.Error()}
		}
	} else if res.Protocol == "ssl" {
		res.Note = strings.TrimSpace(res.Note + " This is an enrollment package (no client certificate). Get a client .p12 from your TAK Server admin and import it too.")
	}
	res.OK = true
	return res
}
