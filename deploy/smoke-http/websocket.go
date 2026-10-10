package main

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
)

// websocket 为真实代理验收提供单条文本回显，不作为产品的 WebSocket 实现。
func websocket(w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("Sec-WebSocket-Key")
	if key == "" || req.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "upgrade required", 400)
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	if rw.Flush() != nil {
		return
	}
	var header [2]byte
	if _, err = io.ReadFull(rw, header[:]); err != nil {
		return
	}
	length := int(header[1] & 127)
	if header[0] != 0x81 || header[1]&128 == 0 || length > 125 {
		return
	}
	var mask [4]byte
	if _, err = io.ReadFull(rw, mask[:]); err != nil {
		return
	}
	data := make([]byte, length)
	if _, err = io.ReadFull(rw, data); err != nil {
		return
	}
	for i := range data {
		data[i] ^= mask[i%4]
	}
	_, _ = rw.Write(append([]byte{0x81, byte(length)}, data...))
	_ = rw.Flush()
}
