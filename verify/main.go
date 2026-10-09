package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	core "github.com/xtls/xray-core/core"
	coreserial "github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	"golang.org/x/net/proxy"
)

const (
	uuid         = "b831381d-6324-4d53-ad4f-8cda48b30811"
	fetchRetries = 5
)

type obj = map[string]any

type scenario struct {
	name   string
	srvIn  obj
	cliOut obj
	udp    bool
}

func freePort(network string) int {
	if network == "udp" {
		l, _ := net.ListenPacket("udp", "127.0.0.1:0")
		defer l.Close()
		return l.LocalAddr().(*net.UDPAddr).Port
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func selfSigned() (certLines, keyLines []string, fingerprint string) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	sum := sha256.Sum256(der)
	return strings.Split(strings.TrimSpace(certPEM), "\n"), strings.Split(strings.TrimSpace(keyPEM), "\n"), hex.EncodeToString(sum[:])
}

func build(config obj) (*core.Instance, error) {
	raw, _ := json.Marshal(config)
	parsed, err := coreserial.LoadJSONConfig(strings.NewReader(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	instance, err := core.New(parsed)
	if err != nil {
		return nil, fmt.Errorf("new: %w", err)
	}
	return instance, nil
}

func start(config obj) (*core.Instance, error) {
	instance, err := build(config)
	if err != nil {
		return nil, err
	}
	if err := instance.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	return instance, nil
}

func fetchThroughSocks(socksPort int, target string) error {
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), &proxy.Auth{User: "u", Password: "p"}, proxy.Direct)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
		},
	}}
	var last error
	for attempt := 0; attempt < fetchRetries; attempt++ {
		response, err := client.Get(target)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) == "pong" {
				return nil
			}
			last = fmt.Errorf("unexpected body %q", body)
		} else {
			last = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last
}

func merge(base obj, extra ...obj) obj {
	for _, e := range extra {
		for k, v := range e {
			base[k] = v
		}
	}
	return base
}

func scenarios(certLines, keyLines []string, pin string) []scenario {
	serverTLS := func(alpn ...string) obj {
		return obj{"security": "tls", "tlsSettings": obj{
			"serverName": "example.com", "alpn": alpn,
			"certificates": []obj{{"certificate": certLines, "key": keyLines}},
		}}
	}
	clientTLS := func(alpn ...string) obj {
		return obj{"security": "tls", "tlsSettings": obj{
			"serverName": "example.com", "fingerprint": "chrome", "alpn": alpn, "pinnedPeerCertSha256": pin,
		}}
	}
	clients := []obj{{"id": uuid}}
	vlessIn := obj{"clients": clients, "decryption": "none"}
	vlessOut := obj{"address": "127.0.0.1", "id": uuid, "encryption": "none"}
	plain := obj{"network": "raw", "security": "none"}
	salamander := obj{"udp": []obj{{"type": "salamander", "settings": obj{"password": "obfs"}}}}
	ss2022 := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	return []scenario{
		{name: "vless_ws_tls",
			srvIn:  obj{"protocol": "vless", "settings": vlessIn, "streamSettings": merge(obj{"network": "ws", "wsSettings": obj{"path": "/ws"}}, serverTLS("http/1.1"))},
			cliOut: obj{"protocol": "vless", "settings": vlessOut, "streamSettings": merge(obj{"network": "ws", "wsSettings": obj{"path": "/ws", "host": "example.com"}}, clientTLS("http/1.1"))}},
		{name: "vless_grpc_tls",
			srvIn:  obj{"protocol": "vless", "settings": vlessIn, "streamSettings": merge(obj{"network": "grpc", "grpcSettings": obj{"serviceName": "svc"}}, serverTLS("h2"))},
			cliOut: obj{"protocol": "vless", "settings": vlessOut, "streamSettings": merge(obj{"network": "grpc", "grpcSettings": obj{"serviceName": "svc", "multiMode": true}}, clientTLS("h2"))}},
		{name: "vless_xhttp_tls",
			srvIn:  obj{"protocol": "vless", "settings": vlessIn, "streamSettings": merge(obj{"network": "xhttp", "xhttpSettings": obj{"path": "/x", "mode": "auto"}}, serverTLS("h2"))},
			cliOut: obj{"protocol": "vless", "settings": vlessOut, "streamSettings": merge(obj{"network": "xhttp", "xhttpSettings": obj{"path": "/x", "mode": "auto", "host": "example.com"}}, clientTLS("h2"))}},
		{name: "vless_httpupgrade_tls",
			srvIn:  obj{"protocol": "vless", "settings": vlessIn, "streamSettings": merge(obj{"network": "httpupgrade", "httpupgradeSettings": obj{"path": "/hu"}}, serverTLS("http/1.1"))},
			cliOut: obj{"protocol": "vless", "settings": vlessOut, "streamSettings": merge(obj{"network": "httpupgrade", "httpupgradeSettings": obj{"path": "/hu", "host": "example.com"}}, clientTLS("http/1.1"))}},
		{name: "vmess_ws_tls",
			srvIn:  obj{"protocol": "vmess", "settings": obj{"clients": clients}, "streamSettings": merge(obj{"network": "ws", "wsSettings": obj{"path": "/v"}}, serverTLS("http/1.1"))},
			cliOut: obj{"protocol": "vmess", "settings": obj{"address": "127.0.0.1", "id": uuid, "security": "auto"}, "streamSettings": merge(obj{"network": "ws", "wsSettings": obj{"path": "/v"}}, clientTLS("http/1.1"))}},
		{name: "trojan_tcp_tls",
			srvIn:  obj{"protocol": "trojan", "settings": obj{"clients": []obj{{"password": "secret"}}}, "streamSettings": merge(obj{"network": "raw"}, serverTLS("h2", "http/1.1"))},
			cliOut: obj{"protocol": "trojan", "settings": obj{"address": "127.0.0.1", "password": "secret"}, "streamSettings": merge(obj{"network": "raw"}, clientTLS("h2", "http/1.1"))}},
		{name: "shadowsocks_aead",
			srvIn:  obj{"protocol": "shadowsocks", "settings": obj{"method": "aes-128-gcm", "password": "secret", "network": "tcp,udp"}},
			cliOut: obj{"protocol": "shadowsocks", "settings": obj{"address": "127.0.0.1", "method": "aes-128-gcm", "password": "secret"}, "streamSettings": plain}},
		{name: "shadowsocks_2022",
			srvIn:  obj{"protocol": "shadowsocks", "settings": obj{"method": "2022-blake3-aes-128-gcm", "password": ss2022, "network": "tcp,udp"}},
			cliOut: obj{"protocol": "shadowsocks", "settings": obj{"address": "127.0.0.1", "method": "2022-blake3-aes-128-gcm", "password": ss2022}, "streamSettings": plain}},
		{name: "socks_outbound",
			srvIn:  obj{"protocol": "socks", "settings": obj{"auth": "password", "accounts": []obj{{"user": "su", "pass": "sp"}}, "udp": true}},
			cliOut: obj{"protocol": "socks", "settings": obj{"address": "127.0.0.1", "user": "su", "pass": "sp"}}},
		{name: "http_outbound",
			srvIn:  obj{"protocol": "http", "settings": obj{"accounts": []obj{{"user": "hu", "pass": "hp"}}}},
			cliOut: obj{"protocol": "http", "settings": obj{"address": "127.0.0.1", "user": "hu", "pass": "hp"}}},
		{name: "hysteria2_salamander", udp: true,
			srvIn:  obj{"protocol": "hysteria", "settings": obj{"version": 2, "clients": []obj{{"auth": "authpass"}}}, "streamSettings": merge(obj{"network": "hysteria", "hysteriaSettings": obj{"version": 2, "auth": "authpass"}, "finalmask": salamander}, serverTLS("h3"))},
			cliOut: obj{"protocol": "hysteria", "settings": obj{"address": "127.0.0.1", "version": 2}, "streamSettings": merge(obj{"network": "hysteria", "hysteriaSettings": obj{"version": 2, "auth": "authpass"}, "finalmask": salamander}, clientTLS("h3"))}},
	}
}

func slimTookEffect() error {
	_, err := build(obj{
		"inbounds": []obj{{"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": freePort("tcp"), "settings": obj{"auth": "noauth"}}},
		"outbounds": []obj{{"tag": "proxy", "protocol": "wireguard", "settings": obj{
			"secretKey": base64.StdEncoding.EncodeToString(make([]byte, 32)), "address": []string{"10.0.0.2/32"},
			"peers": []obj{{"publicKey": base64.StdEncoding.EncodeToString(make([]byte, 32)), "endpoint": "198.51.100.7:51820"}}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		return fmt.Errorf("WireGuard is still available, the slim patch did not apply (%v)", err)
	}
	return nil
}

func realityParses() error {
	key := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	_, err := build(obj{
		"log":      obj{"loglevel": "none"},
		"inbounds": []obj{{"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": freePort("tcp"), "settings": obj{"auth": "noauth"}}},
		"outbounds": []obj{{"tag": "proxy", "protocol": "vless",
			"settings": obj{"address": "198.51.100.7", "port": 443, "id": uuid, "encryption": "none", "flow": "xtls-rprx-vision"},
			"streamSettings": obj{"network": "raw", "security": "reality", "realitySettings": obj{
				"serverName": "www.microsoft.com", "fingerprint": "chrome", "publicKey": key, "shortId": "6ba85179e30d4fc2", "spiderX": "/"}}}},
	})
	return err
}

func main() {
	fmt.Println("xray-core", core.Version())
	certLines, keyLines, pin := selfSigned()
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	go http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("pong")) }))
	target := fmt.Sprintf("http://%s/", listener.Addr().String())
	allowLoopback := obj{"protocol": "freedom", "settings": obj{"finalRules": []obj{{"action": "allow"}}}}

	failed := 0
	report := func(name string, err error) {
		if err != nil {
			failed++
			fmt.Printf("%-26s FAIL %v\n", name, err)
			return
		}
		fmt.Printf("%-26s OK\n", name)
	}
	report("slim_patch_applied", slimTookEffect())
	report("vless_reality_parse", realityParses())
	for _, s := range scenarios(certLines, keyLines, pin) {
		network := "tcp"
		if s.udp {
			network = "udp"
		}
		serverPort, clientPort := freePort(network), freePort("tcp")
		s.srvIn["tag"], s.srvIn["listen"], s.srvIn["port"] = "in", "127.0.0.1", serverPort
		s.cliOut["tag"] = "proxy"
		s.cliOut["settings"].(obj)["port"] = serverPort
		server, err := start(obj{"log": obj{"loglevel": "none"}, "inbounds": []obj{s.srvIn}, "outbounds": []obj{allowLoopback}})
		if err != nil {
			report(s.name, fmt.Errorf("server %w", err))
			continue
		}
		client, err := start(obj{
			"log":       obj{"loglevel": "none"},
			"inbounds":  []obj{{"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": clientPort, "settings": obj{"auth": "password", "accounts": []obj{{"user": "u", "pass": "p"}}, "udp": false}}},
			"outbounds": []obj{s.cliOut},
		})
		if err != nil {
			server.Close()
			report(s.name, fmt.Errorf("client %w", err))
			continue
		}
		report(s.name, fetchThroughSocks(clientPort, target))
		client.Close()
		server.Close()
	}
	if failed > 0 {
		os.Exit(1)
	}
}
