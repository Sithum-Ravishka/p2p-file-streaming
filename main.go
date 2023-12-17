package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/iden3/go-merkletree-sql"
	"github.com/iden3/go-merkletree-sql/db/memory"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	peerstore "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	multiaddr "github.com/multiformats/go-multiaddr"
)

const (
	counterProtocol   = "/counter"
	dataFilePath      = "./test/text.png"
	signalEndOfFile   = uint64(0)
	merkleTreeLevels  = 32
	sleepDurationSecs = 1
	// New custom protocol for broadcasting file shares and connected peers
	broadcastProtocol = "/broadcast"
)

func main() {

	// start a libp2p node that listens on a random local TCP port,
	// but without running the built-in ping protocol
	node, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Ping(false),
	)
	if err != nil {
		panic(err)
	}

	// Register the "/broadcast" protocol for broadcasting file shares and connected peers
	node.SetStreamHandler(broadcastProtocol, handleBroadcastStream)

	// configure our own ping protocol
	pingService := &ping.PingService{Host: node}
	node.SetStreamHandler(ping.ID, pingService.PingHandler)

	// Register the "/counter" protocol
	node.SetStreamHandler(counterProtocol, handleCounterStream)

	// print the node's PeerInfo in multiaddr format
	peerInfo := peerstore.AddrInfo{
		ID:    node.ID(),
		Addrs: node.Addrs(),
	}
	addrs, err := peerstore.AddrInfoToP2pAddrs(&peerInfo)
	if err != nil {
		panic(err)
	}
	fmt.Println("libp2p node address:", addrs[0])

	// if a remote peer has been passed on the command line, connect to it
	// and send/receive counter values, otherwise wait for a signal to stop
	if len(os.Args) > 1 {
		addr, err := multiaddr.NewMultiaddr(os.Args[1])
		if err != nil {
			panic(err)
		}
		peer, err := peerstore.AddrInfoFromP2pAddr(addr)
		if err != nil {
			panic(err)
		}
		if err := node.Connect(context.Background(), *peer); err != nil {
			panic(err)
		}
		fmt.Println("sending and receiving counter values with", addr)

		// Open a stream to the remote peer with the "/counter" protocol
		stream, err := node.NewStream(context.Background(), peer.ID, counterProtocol)
		if err != nil {
			panic(err)
		}

		// Run the writeCounter and readCounter functions concurrently
		go writeCounter(stream)

		// Run the broadcast function concurrently to inform other peers about the new connection
		go broadcastConnectedPeer(node, addr)
		readCounter(stream)
	} else {
		// wait for a SIGINT or SIGTERM signal
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		fmt.Println("Received signal, shutting down...")
	}

	// shut the node down
	if err := node.Close(); err != nil {
		panic(err)
	}

}

// Function to broadcast information about the new connected peer to all peers
func broadcastConnectedPeer(node host.Host, addr multiaddr.Multiaddr) {
	// Get the list of all connected peers
	peers := node.Network().Peers()

	// Iterate over all connected peers and send broadcast messages
	for _, peer := range peers {
		if peer == node.ID() {
			continue // Skip broadcasting to itself
		}

		// Open a stream to the remote peer with the "/broadcast" protocol
		stream, err := node.NewStream(context.Background(), peer, broadcastProtocol)
		if err != nil {
			fmt.Println("Error opening broadcast stream to peer", peer, ":", err)
			continue
		}

		// Send information about the new connected peer
		fmt.Fprintf(stream, "New peer connected: %s\n", addr.String())
		stream.Close()
	}
}

// Function to handle the "/broadcast" protocol stream
func handleBroadcastStream(stream network.Stream) {
	defer stream.Close()

	// Read and print the broadcast message
	data, err := ioutil.ReadAll(stream)
	if err != nil {
		fmt.Println("Error reading broadcast message:", err)
		return
	}
	fmt.Println("Broadcast message received:", string(data))
}

func handleCounterStream(stream network.Stream) {
	fmt.Println("New incoming counter stream from", stream.Conn().RemotePeer())
	// Run the readCounter function for incoming streams
	readCounter(stream)
}

func writeCounter(s network.Stream) {
	file, err := os.Open(dataFilePath)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	buffer := make([]byte, 90240)

	// Initialize Merkle Tree
	ctx := context.Background()
	store := memory.NewMemoryStorage()
	mt, _ := merkletree.NewMerkleTree(ctx, store, merkleTreeLevels)

	for {
		n, err := file.Read(buffer)
		if err == io.EOF {
			// Signal end of file by sending a counter value of 0
			err = binary.Write(s, binary.BigEndian, signalEndOfFile)
			if err != nil {
				panic(err)
			}
			fmt.Println("End of file reached.")
			break
		} else if err != nil {
			panic(err)
		}

		// Write the binary-encoded counter value and file data to the stream
		err = binary.Write(s, binary.BigEndian, uint64(n))
		if err != nil {
			panic(err)
		}

		_, err = s.Write(buffer[:n])
		if err != nil {
			panic(err)
		}

		// Convert the buffer to a big.Int before adding it to the Merkle Tree
		dataAsBigInt := new(big.Int).SetBytes(buffer[:n])
		index := big.NewInt(int64(n))
		mt.Add(ctx, index, dataAsBigInt)

		// Sleep for a while
		time.Sleep(time.Second * sleepDurationSecs)
	}

	// Generate proof for the root of the Merkle Tree
	root := mt.Root()
	proofExist, _, _ := mt.GenerateProof(ctx, big.NewInt(0), root)

	// Print Merkle Tree proof information
	fmt.Println("Merkle Tree Root:", root)
	fmt.Println("Proof of membership:", proofExist.Existence)
}

func readCounter(s network.Stream) {
	peerAddr := s.Conn().RemoteMultiaddr().String()
	peerAddr = sanitizeAddress(peerAddr) // Sanitize the address for folder name

	// Create a folder for the peer if it doesn't exist
	if err := os.MkdirAll(peerAddr, 0755); err != nil {
		panic(err)
	}

	for {
		var counter uint64

		// Read the binary-encoded counter value from the stream
		err := binary.Read(s, binary.BigEndian, &counter)
		if err != nil {
			if err == io.EOF {
				fmt.Println("Remote peer closed the stream.")
				break
			}
			panic(err)
		}

		// Check if it's the signal for the end of the file
		if counter == signalEndOfFile {
			fmt.Println("End of file signal received.")
			break
		}

		fmt.Printf("Received %d bytes from %s\n", counter, s.Conn().RemotePeer())

		// Read the file data from the stream
		buffer := make([]byte, counter)
		_, err = io.ReadFull(s, buffer)
		if err != nil {
			panic(err)
		}

		filePath := fmt.Sprintf("%s/chunk_%d.txt", peerAddr, time.Now().UnixNano())
		err = ioutil.WriteFile(filePath, buffer, 0644)
		if err != nil {
			panic(err)
		}

		fmt.Printf("Saved chunk to: %s\n", filePath)
	}
}

func sanitizeAddress(address string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9]+`)
	return re.ReplaceAllString(address, "_")
}
