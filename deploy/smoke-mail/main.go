// 此测试服务只接收隔离发布测试的邮件，从不转发或连接外部邮箱。
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type message struct {
	To   string `json:"to"`
	Code string `json:"code"`
}
type mailbox struct {
	sync.Mutex
	messages []message
}

var loginCode = regexp.MustCompile(`StackNest login code: ([0-9]{8})`)

// certificate 创建仅本次测试信任的短期证书，生产 SMTP 仍执行正常的 TLS 验证。
func certificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "StackNest isolated release test"},
		DNSNames: []string{"release-mail"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.MkdirAll("/certs", 0755); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile("/certs/ca.pem", public, 0644); err != nil {
		return tls.Certificate{}, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(public, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
}

// receive 处理测试客户端使用的 SMTP 命令，只接收虚构管理员的验证码。
func (box *mailbox) receive(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(20 * time.Second))
	reader := textproto.NewReader(bufio.NewReader(io.LimitReader(connection, 256<<10)))
	writer := textproto.NewWriter(bufio.NewWriter(connection))
	_ = writer.PrintfLine("220 release-mail ESMTP")
	authorized, sender, recipient := false, false, false
	for {
		line, err := reader.ReadLine()
		if err != nil {
			return
		}
		command, argument, _ := strings.Cut(line, " ")
		switch strings.ToUpper(command) {
		case "EHLO":
			_ = writer.PrintfLine("250-release-mail\r\n250 AUTH PLAIN")
		case "AUTH":
			mechanism, encoded, _ := strings.Cut(argument, " ")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			authorized = mechanism == "PLAIN" && err == nil && string(decoded) == "\x00smtp@example.com\x00release-mail-only"
			if !authorized {
				_ = writer.PrintfLine("535 Authentication failed")
				return
			}
			_ = writer.PrintfLine("235 Authenticated")
		case "MAIL":
			sender = authorized && argument == "FROM:<smtp@example.com>"
			if !sender {
				_ = writer.PrintfLine("550 Test sender only")
				return
			}
			_ = writer.PrintfLine("250 OK")
		case "RCPT":
			recipient = sender && argument == "TO:<admin@example.com>"
			if !recipient {
				_ = writer.PrintfLine("550 Test recipient only")
				return
			}
			_ = writer.PrintfLine("250 OK")
		case "DATA":
			if !recipient {
				_ = writer.PrintfLine("503 Recipient required")
				return
			}
			_ = writer.PrintfLine("354 End with dot")
			body, err := reader.ReadDotBytes()
			code := loginCode.FindSubmatch(body)
			if err != nil || len(code) != 2 {
				_ = writer.PrintfLine("550 Expected test login code")
				return
			}
			box.Lock()
			if len(box.messages) < 10 {
				box.messages = append(box.messages, message{To: "admin@example.com", Code: string(code[1])})
			}
			box.Unlock()
			_ = writer.PrintfLine("250 Stored")
		case "QUIT":
			_ = writer.PrintfLine("221 Bye")
			return
		default:
			_ = writer.PrintfLine("502 Unsupported")
		}
	}
}

// ServeHTTP 只暴露健康检查与内存收件箱，端口由测试编排绑定到本机回环。
func (box *mailbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/health":
		_, _ = fmt.Fprint(w, "ok")
	case "/inbox":
		w.Header().Set("Content-Type", "application/json")
		box.Lock()
		defer box.Unlock()
		_ = json.NewEncoder(w).Encode(box.messages)
	default:
		http.NotFound(w, r)
	}
}

// main 启动独立的 TLS 收信与回环发布的查询接口，health 模式用于 Compose 就绪检查。
func main() {
	if len(os.Args) == 2 && os.Args[1] == "health" {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://127.0.0.1:8025/health")
		if err != nil {
			os.Exit(1)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	cert, err := certificate()
	if err != nil {
		log.Fatal(err)
	}
	listener, err := tls.Listen("tcp", ":2465", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		log.Fatal(err)
	}
	box := &mailbox{messages: []message{}}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go box.receive(connection)
		}
	}()
	server := &http.Server{Addr: ":8025", Handler: box, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
