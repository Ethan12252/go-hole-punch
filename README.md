# UDP NAT Hole Punching (Go)

A simple UDP signaling server and client implementation for demonstrating NAT hole punching.

## Protocol

Message format: single-character prefix + optional payload
- `R<PeerID>`: Register
- `A<IP>:<Port>`: Server to Peer: peer address
- `H<PeerID>`: Hole punch
- `SOK`: Success acknowledge
- `K<PeerID>`: Keep-Alive

## Usage

Start the server:
```sh
go run cmd/server/server.go -addr :8000
```

Start client A:
```sh
go run cmd/client/client.go -server 127.0.0.1:8000 A
```

Start client B:
```sh
go run cmd/client/client.go -server 127.0.0.1:8000 B
```

## Build

```sh
go build -o server ./cmd/server
go build -o client ./cmd/client
```
