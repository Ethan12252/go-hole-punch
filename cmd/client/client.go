package main

import (
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// Client holds runtime state
type Client struct {
	conn           *net.UDPConn
	peerID         string
	serverAddr     *net.UDPAddr
	targetPeerAddr *net.UDPAddr
	isConnected    bool
	mu             sync.Mutex
	keepAliveOnce  sync.Once

	// configuration for punching
	punchAttempts int
	punchInterval time.Duration
}

func main() {
	log.SetFlags(log.LstdFlags)
	// Add a flag to configure the server address to use
	serverFlag := flag.String("server", "127.0.0.1:8000", "server address in host:port format (UDP)")
	punchAttempts := flag.Int("punchAttempts", 500, "how many hole-punch packets to send")
	punchIntervalMs := flag.Int("punchIntervalMs", 50, "interval between hole-punch packets in milliseconds")
	reRegSec := flag.Int("reRegSec", 3, "re-register interval in seconds to keep NAT mapping alive")
	natTest := flag.Bool("natTest", false, "run a simple STUN NAT detection test and report the results; helpful for debugging NAT types")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Println("Usage: client.go -server <server-ip:port> <PeerID> ")
		os.Exit(1)
	}
	peerID := flag.Arg(0)
	if len(peerID) < 1 {
		fmt.Println("PeerID must be at least one char (A/B)")
		os.Exit(1)
	}
	peerID = string(peerID[0])

	laddr := &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		log.Fatalf("建立 UDP 連線失敗: %v", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	log.Printf("客戶端 %s 啟動，本地地址: %s", peerID, localAddr.String())

	// resolve server address from flag
	log.Printf("解析伺服器地址: %s", *serverFlag)
	serverAddr, err := net.ResolveUDPAddr("udp4", *serverFlag)
	if err != nil {
		log.Fatalf("解析伺服器地址失敗: %v", err)
	}

	client := &Client{
		conn:          conn,
		peerID:        peerID,
		serverAddr:    serverAddr,
		punchAttempts: *punchAttempts,
		punchInterval: time.Duration(*punchIntervalMs) * time.Millisecond,
	}

	// Optionally run STUN NAT detection before registering
	if *natTest {
		log.Printf("執行 STUN NAT 檢測...")
		summary, err := runSimpleStunTests(client.conn)
		if err != nil {
			log.Printf("STUN 測試失敗: %v", err)
		} else {
			log.Printf("STUN 測試結果: %s", summary)
		}
	}

	client.sendRegister()

	// re-register every few seconds until we get a target address (keeps NAT mapping alive)
	go func() {
		ticker := time.NewTicker(time.Duration(*reRegSec) * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			client.mu.Lock()
			hasTarget := client.targetPeerAddr != nil
			client.mu.Unlock()
			if hasTarget {
				return
			}
			client.sendRegister()
		}
	}()

	// warning goroutine for 10s timeout
	go func() {
		t := time.NewTimer(10 * time.Second)
		<-t.C
		client.mu.Lock()
		if client.targetPeerAddr == nil {
			log.Printf("警告: 10 秒內未收到對方地址，請確認伺服器或另一端是否已啟動")
		}
		client.mu.Unlock()
	}()

	client.receiveLoop()
}

func (c *Client) sendRegister() {
	msg := []byte("R" + c.peerID)
	// Log local and remote endpoints so we can debug NAT mappings
	if c.conn != nil && c.conn.LocalAddr() != nil {
		log.Printf("註冊使用本地地址: %s -> %s", c.conn.LocalAddr().String(), c.serverAddr.String())
	}
	log.Printf("向伺服器註冊... (%s)", string(msg))
	if _, err := c.conn.WriteToUDP(msg, c.serverAddr); err != nil {
		log.Printf("向伺服器註冊失敗: %v", err)
	}
}

func (c *Client) receiveLoop() {
	buf := make([]byte, 1024)
	for {
		n, src, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("ReadFromUDP error: %v", err)
			continue
		}
		if n <= 0 {
			continue
		}
		// Debug: log all incoming packets with source and payload
		log.Printf("收到 %d bytes 來自 %s: %q", n, src.String(), string(buf[:n]))
		msg := string(buf[:n])

		// Protect accesses to msg[0]
		if len(msg) == 0 {
			continue
		}

		// Distinguish between server and peer messages
		// Use string equality for the server address because IP/Port may be represented differently
		if src.String() == c.serverAddr.String() {
			// From server: expect P<PeerID><ip:port>
			if msg[0] == 'P' {
				if len(msg) < 3 {
					log.Printf("伺服器傳送的地址資訊長度不足: %q", msg)
					continue
				}
				peerID := string(msg[1])
				addrStr := msg[2:]
				addr, err := net.ResolveUDPAddr("udp4", addrStr)
				if err != nil {
					log.Printf("解析對方地址失敗: %v", err)
					continue
				}
				c.mu.Lock()
				c.targetPeerAddr = addr
				// reset isConnected flag as new target
				c.isConnected = false
				c.mu.Unlock()
				log.Printf("收到對方地址: %s (PeerID=%s)，立即啟動打洞...", addr.String(), peerID)
				// start hole punching goroutine
				go c.holePunchingGoroutine()
			}
			continue
		}

		// If message from target peer addr
		c.mu.Lock()
		target := c.targetPeerAddr
		c.mu.Unlock()
		if target != nil && target.String() == src.String() {
			// Peer message types
			switch msg[0] {
			case 'H':
				// received hole punch from peer
				log.Printf("收到對方打洞封包: %s", msg)
				c.mu.Lock()
				if !c.isConnected {
					c.isConnected = true
					c.mu.Unlock()
					// Send success SOK back
					if _, err := c.conn.WriteToUDP([]byte("SOK"), target); err != nil {
						log.Printf("回應 SOK 失敗: %v", err)
					}
					log.Printf("收到打洞回應！P2P 連線建立成功！開始直接通訊。")
					// start keep-alive goroutine once
					c.keepAliveOnce.Do(func() { go c.keepAliveGoroutine() })
				} else {
					c.mu.Unlock()
				}
			case 'S':
				if msg == "SOK" {
					c.mu.Lock()
					if !c.isConnected {
						c.isConnected = true
						c.mu.Unlock()
						log.Printf("對方已接受連線")
						c.keepAliveOnce.Do(func() { go c.keepAliveGoroutine() })
					} else {
						c.mu.Unlock()
					}
				}
			case 'K':
				log.Printf("收到對方 Keep-Alive: %s", msg)
			default:
				log.Printf("收到未知的 Peer 訊息: %s", msg)
			}
		}
	}
}

func (c *Client) holePunchingGoroutine() {
	c.mu.Lock()
	target := c.targetPeerAddr
	attempts := c.punchAttempts
	interval := c.punchInterval
	c.mu.Unlock()
	if target == nil {
		return
	}
	log.Printf("開始向對方發送打洞封包: %s (attempts=%d interval=%v)", target.String(), attempts, interval)
	for i := 0; i < attempts; i++ {
		c.mu.Lock()
		if c.isConnected {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		// send H<PeerID>
		msg := []byte("H" + c.peerID)
		if i < 5 { // log the first few sends for debug
			log.Printf("打洞發送 %d -> %s: %q", i+1, target.String(), string(msg))
		}
		if _, err := c.conn.WriteToUDP(msg, target); err != nil {
			log.Printf("發送打洞封包失敗: %v", err)
		}
		if i == 0 {
			// only print the first time
			// already printed above
		}
		// configurable interval between attempts
		time.Sleep(interval)
	}
	log.Printf("打洞階段完成，等待對方回應... (sent %d attempts)", attempts)
}

func (c *Client) keepAliveGoroutine() {
	// Only started once
	for {
		c.mu.Lock()
		connected := c.isConnected
		target := c.targetPeerAddr
		c.mu.Unlock()
		if !connected || target == nil {
			// wait and retry
			time.Sleep(1 * time.Second)
			continue
		}
		// Send K<PeerID>
		if _, err := c.conn.WriteToUDP([]byte("K"+c.peerID), target); err != nil {
			log.Printf("發送 Keep-Alive 失敗: %v", err)
		} else {
			log.Printf("發送 Keep-Alive 訊息")
		}
		time.Sleep(2 * time.Second)
	}
}

// runSimpleStunTests performs a couple of STUN Binding requests to help classify NAT type.
// It's a minimal implementation sufficient for basic diagnostics. It performs:
// 1) Binding request to a primary STUN server (get external addr)
// 2) Binding request to a secondary STUN server (compare mapping)
// 3) Binding request to primary server with CHANGE-REQUEST (both IP+port) to check for full-cone
// NOTE: This test is not as thorough as full RFC3489/5389 testing but helps detect symmetric NAT.
func runSimpleStunTests(conn *net.UDPConn) (string, error) {
	servers := []string{"stun.l.google.com:19302", "stun1.l.google.com:19302"}
	magicCookie := uint32(0x2112A442)
	// helper to send request and parse XOR-MAPPED-ADDRESS
	doBinding := func(server string, changeRequest uint32) (*net.UDPAddr, error) {
		// Build STUN binding request message
		txn := make([]byte, 12)
		if _, err := rand.Read(txn); err != nil {
			return nil, err
		}
		// header
		msg := make([]byte, 20)
		// Type = Binding Request (0x0001)
		binary.BigEndian.PutUint16(msg[0:2], 0x0001)
		// Length will be set later
		// Magic Cookie
		binary.BigEndian.PutUint32(msg[4:8], magicCookie)
		copy(msg[8:20], txn)
		// Optional: ChangeRequest attribute (type 0x0003)
		if changeRequest != 0 {
			attr := make([]byte, 8)
			binary.BigEndian.PutUint16(attr[0:2], 0x0003)
			binary.BigEndian.PutUint16(attr[2:4], 4)
			binary.BigEndian.PutUint32(attr[4:8], changeRequest)
			msg = append(msg, attr...)
			binary.BigEndian.PutUint16(msg[2:4], uint16(len(msg)-20))
		}
		// Resolve server addr
		raddr, err := net.ResolveUDPAddr("udp4", server)
		if err != nil {
			return nil, err
		}
		// send
		if _, err := conn.WriteToUDP(msg, raddr); err != nil {
			return nil, err
		}
		// set deadline and read
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		defer conn.SetReadDeadline(time.Time{})
		buf := make([]byte, 1024)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		res := buf[:n]
		// check header & transaction id
		if n < 20 {
			return nil, fmt.Errorf("stun response too short: %d", n)
		}
		// Check transaction ID matches
		if string(res[8:20]) != string(txn) {
			return nil, fmt.Errorf("txn mismatch in stun response")
		}
		// parse attributes
		pos := 20
		for pos+4 <= n {
			t := binary.BigEndian.Uint16(res[pos : pos+2])
			l := int(binary.BigEndian.Uint16(res[pos+2 : pos+4]))
			pos += 4
			if pos+l > n {
				break
			}
			if t == 0x0020 { // XOR-MAPPED-ADDRESS
				if l < 8 {
					break
				}
				fam := res[pos+1]
				if fam != 0x01 { // IPv4 only
					break
				}
				xPort := binary.BigEndian.Uint16(res[pos+2 : pos+4])
				port := xPort ^ uint16(magicCookie>>16)
				xAddr := binary.BigEndian.Uint32(res[pos+4 : pos+8])
				addrInt := xAddr ^ magicCookie
				ip := net.IPv4(byte(addrInt>>24), byte(addrInt>>16), byte(addrInt>>8), byte(addrInt)).To4()
				return &net.UDPAddr{IP: ip, Port: int(port)}, nil
			}
			// advance pos (4 byte alignment)
			pad := ((l + 3) / 4) * 4
			pos += pad
		}
		// fallback: try MAPPED-ADDRESS (old attribute 0x0001)
		pos = 20
		for pos+4 <= n {
			t := binary.BigEndian.Uint16(res[pos : pos+2])
			l := int(binary.BigEndian.Uint16(res[pos+2 : pos+4]))
			pos += 4
			if pos+l > n {
				break
			}
			if t == 0x0001 { // MAPPED-ADDRESS
				if l < 8 {
					break
				}
				fam := res[pos+1]
				if fam != 0x01 {
					break
				}
				port := int(binary.BigEndian.Uint16(res[pos+2 : pos+4]))
				ip := net.IPv4(res[pos+4], res[pos+5], res[pos+6], res[pos+7])
				return &net.UDPAddr{IP: ip, Port: port}, nil
			}
			pad := ((l + 3) / 4) * 4
			pos += pad
		}
		return nil, fmt.Errorf("no XOR-MAPPED-ADDRESS in stun response")
	}

	// Do two binding requests to two different servers
	a, err := doBinding(servers[0], 0)
	if err != nil {
		return "", err
	}
	b, err := doBinding(servers[1], 0)
	if err != nil {
		return "", err
	}
	// Now send change-request to servers[0]
	changeBoth := uint32(0x00000006) // change ip & change port bits
	changeResp, _ := doBinding(servers[0], changeBoth)

	local := conn.LocalAddr().(*net.UDPAddr)
	// Make a simple classification
	if a.IP.Equal(local.IP) && a.Port == local.Port {
		return fmt.Sprintf("Open Internet (no NAT). Local=%s, STUN=%s", local.String(), a.String()), nil
	}
	if a.String() != b.String() {
		return fmt.Sprintf("Symmetric NAT detected. STUN A=%s, STUN B=%s; Local=%s", a.String(), b.String(), local.String()), nil
	}
	if changeResp != nil {
		return fmt.Sprintf("Full Cone NAT (or NAT that allows change-request). STUN=%s; Local=%s", a.String(), local.String()), nil
	}
	// if mapped addresses match across servers and change-request didn't respond, likely restricted/port-restricted
	return fmt.Sprintf("Restricted NAT (could be restricted/port-restricted). STUN=%s; Local=%s", a.String(), local.String()), nil
}
