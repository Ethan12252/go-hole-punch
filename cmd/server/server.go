package main

import (
	"flag"
	"log"
	"net"
	"sync"
	"time"
)

// Server holds UDP connection and peer map
type Server struct {
	conn    *net.UDPConn
	peerMap map[string]*net.UDPAddr
	mu      sync.Mutex
}

func main() {
	log.SetFlags(log.LstdFlags)
	// addr flag allows listening on a configurable address
	listenAddr := flag.String("addr", ":8000", "UDP listen address (host:port)")
	flag.Parse()

	// Resolve and bind explicitly to IPv4 so we prefer 0.0.0.0:PORT instead of [::]:PORT
	// This keeps logs clearer when the host has IPv6 disabled.
	addr, err := net.ResolveUDPAddr("udp4", *listenAddr)
	if err != nil {
		log.Fatalf("解析地址失敗: %v", err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatalf("監聽失敗: %v", err)
	}
	defer conn.Close()

	server := &Server{
		conn:    conn,
		peerMap: make(map[string]*net.UDPAddr),
	}

	// Print both IPv4 and IPv6 (mapped) forms in the logs for clarity.
	if udpAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		var ipv4Str, ipv6Str string
		if ip4 := udpAddr.IP.To4(); ip4 != nil {
			ipv4Str = ip4.String()
			// IPv6-mapped representation for IPv4: ::ffff:x.x.x.x
			ipv6Str = "::ffff:" + ip4.String()
		} else {
			// If it isn't IPv4, print what we have
			ipv6Str = udpAddr.IP.String()
		}
		log.Printf("中介伺服器啟動，監聽於 %s (IPv4=%s:%d IPv6=%s:%d)", conn.LocalAddr().String(), ipv4Str, udpAddr.Port, ipv6Str, udpAddr.Port)
	} else {
		log.Printf("中介伺服器啟動，監聽於 %s", conn.LocalAddr().String())
	}

	// Run the read loop
	for {
		buf := make([]byte, 1024)
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("ReadFromUDP error: %v", err)
			continue
		}
		// Protect from panics during packet handling
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered in handler: %v", r)
				}
			}()

			if n <= 0 {
				return
			}
			msg := string(buf[:n])
			// Debug: log every incoming packet so we can see registrations & unexpected packets
			log.Printf("收到 %d bytes 來自 %s: %q", n, remoteAddr.String(), msg)
			if msg == "" {
				return
			}
			switch msg[0] {
			case 'R':
				// Register: R<PeerID>
				if len(msg) < 2 {
					log.Printf("Invalid register message from %s: %q", remoteAddr.String(), msg)
					return
				}
				peerID := string(msg[1])
				server.mu.Lock()
				server.peerMap[peerID] = remoteAddr
				server.mu.Unlock()
				log.Printf("客戶端 %s 註冊成功，公網地址: %s", peerID, remoteAddr.String())

				// If both A and B exist, exchange addresses
				server.mu.Lock()
				addrA, aOK := server.peerMap["A"]
				addrB, bOK := server.peerMap["B"]
				server.mu.Unlock()
				if aOK && bOK {
					// Use 'P' as the address-exchange marker: P<PeerID><ip:port>
					// send B->A and A->B
					msgToA := []byte("P" + "B" + addrB.String())
					msgToB := []byte("P" + "A" + addrA.String())
					// send to A
					if _, err := conn.WriteToUDP(msgToA, addrA); err != nil {
						log.Printf("WriteToUDP to A failed: %v", err)
					}
					log.Printf("發送給 A (%s) => %q", addrA.String(), string(msgToA))
					// send to B
					if _, err := conn.WriteToUDP(msgToB, addrB); err != nil {
						log.Printf("WriteToUDP to B failed: %v", err)
					}
					log.Printf("發送給 B (%s) => %q", addrB.String(), string(msgToB))
					log.Printf("地址交換完成。發送 A 地址給 B，發送 B 地址給 A")
				}
			default:
				// Ignore other messages
				log.Printf("忽略來自 %s 的未知訊息: %q", remoteAddr.String(), msg)
			}
		}()
		// Small backoff
		time.Sleep(10 * time.Millisecond)
	}
}
