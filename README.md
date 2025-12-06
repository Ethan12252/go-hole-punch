# UDP NAT Hole Punching (Go)

This repository implements a simple UDP signaling server and two clients to demonstrate UDP NAT hole punching. The implementation follows the provided spec, with logging in Chinese messages and required behavior.

## Files
- `server.go`: Signaling server that registers peers and exchanges public addresses.
- `client.go`: Peer client that registers with the server, receives the other peer's public address, performs hole punching, responds to successful holepunch, and maintains keep-alive messages.

## Protocol
Message format: single-character prefix + optional payload
- `R<PeerID>`: Register
- `A<IP>:<Port>`: Server -> Peer: peer address
- `H<PeerID>`: Hole punch
- `SOK`: Success acknowledge (S)
- `K<PeerID>`: Keep-Alive

## How to Run (msys2 / Windows)
Start the server (in one terminal):

```sh
go run cmd/server/server.go -addr :8000
```

Start client A (in another terminal):

```sh
go run cmd/client/client.go -server 127.0.0.1:8000 A
```

Start client B (in another terminal):

```sh
go run cmd/client/client.go -server 127.0.0.1:8000 B
```

You should see logs like:
- Server: "中介伺服器啟動，監聽於 :8000"
- Client: "客戶端 A 啟動，本地地址: ..."
- Server: "客戶端 A 註冊成功，公網地址: ..."
- Server: "地址交換完成。發送 A 地址給 B，發送 B 地址給 A"
- Clients: "收到對方地址...，立即啟動打洞..."
- Clients: "開始向對方發送打洞封包..."
- Clients: "收到打洞回應！P2P 連線建立成功！開始直接通訊。"
- Clients: "發送 Keep-Alive 訊息"

## Notes
- By default, the client will use `127.0.0.1:8000`. In an environment with NAT you should point the client to the public IP of the server.
- The server currently matches `A` and `B` peers for exchange. Re-registration will overwrite the existing mapping.

## Development
- Use `go build` to compile `server.go` and `client.go` into binaries:

```sh
# build server
go build -o server.exe ./cmd/server
# build client
go build -o client.exe ./cmd/client
```

Run the compiled binaries instead of `go run` for better performance.

## Troubleshooting
- If clients can't see each other, ensure that your firewall isn't blocking UDP port 8000 or peer-to-peer UDP traffic.
- If using NAT, ensure port forwarding and NAT behavior are as expected (not symmetric NAT).

## Cross-build and deploy to Linux (GCP) ✅

1. Cross-build the binaries for Linux (amd64):

```sh
# From msys2 bash
export GOOS=linux GOARCH=amd64
go build -o server-linux ./cmd/server
go build -o client-linux ./cmd/client
```

2. Create a small GCP VM (or use an existing one). Allow UDP ingress on port 8000 via the VPC firewall or via `gcloud`:

```sh
gcloud compute firewall-rules create allow-udp-8000 --allow udp:8000 --direction INGRESS --source-ranges 0.0.0.0/0 --network default
```

3. Upload the server binary to the VM and run it. Example:

```sh
scp server-linux USER@VM_IP:~/server
ssh USER@VM_IP
chmod +x server
sudo ufw allow 8000/udp    # If using ufw
./server -addr :8000
```

4. Run clients (from different endpoints/networks) and point them at the VM public IP:

```sh
# on each client host
scp client-linux USER@CLIENT_HOST:~/client
ssh USER@CLIENT_HOST
chmod +x client
./client -server VM_PUBLIC_IP:8000 A
./client -server VM_PUBLIC_IP:8000 B
```

## Networking / Firewall Notes ⚠️

- GCP firewall rules are required for inbound UDP on the server port (default 8000). Add an ingress rule for UDP 8000.
- The VM OS-level firewall (iptables/ufw) must also allow UDP port 8000.
- Clients must be able to send outbound UDP. For hole punching to work, at least each client needs to have an upstream NAT that creates a stable endpoint mapping (not symmetric NAT in both directions).
- If the server is behind Cloud NAT or private subnet, ensure the server is reachable from public clients or set up a public IP for the server VM.

## Testing & troubleshooting 🔧

- Verify server is listening and visible from the public internet:

```sh
ss -u -n -a | grep 8000
sudo tcpdump -n -i any udp port 8000
```

- Look for registration logs on the server when a client starts:

Expected server logs:
- `客戶端 A 註冊成功，公網地址: ...`
- `地址交換完成。發送 A 地址給 B，發送 B 地址給 A`

- Look for client logs:
- `解析伺服器地址: <ip:port>` and `向伺服器註冊...` at startup
- `收到對方地址` and `開始向對方發送打洞封包...` when the server successfully provided peer address

- If hole punching does not work:
	1. Confirm both clients registered with the server (server logs include registration).
	2. Confirm the server sent address info to peers (server logs show WriteToUDP calls).
	3. Confirm clients could reach server (check logs for errors resolving or writing to UDP address).
	4. Check whether the NAT is symmetric; symmetric NATs often break UDP hole punching. Try one client on a different network (mobile tether / no NAT) to verify.
	5. If you need low-level tracing, use `tcpdump` on the clients or server to see whether UDP packets are leaving/arriving.

## Notes
- When running in production, you should run the server behind a proper process supervisor (systemd) and ensure logs are centrally collected.
- For robust P2P connectivity in varied NAT setups, consider adding a TURN relay or use a public STUN/TURN infrastructure.
