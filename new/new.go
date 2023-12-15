package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	peerstore "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"
)

const (
	counterProtocol = "/counter"
	fileProtocol    = "/file"
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

	// configure our own ping protocol
	pingService := &ping.PingService{Host: node}
	node.SetStreamHandler(ping.ID, pingService.PingHandler)

	// Register the "/counter" and "/file" protocols
	node.SetStreamHandler(counterProtocol, handleCounterStream)
	node.SetStreamHandler(fileProtocol, handleFileStream)

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

		// Open a stream to the remote peer with the "/file" protocol
		fileStream, err := node.NewStream(context.Background(), peer.ID, fileProtocol)
		if err != nil {
			panic(err)
		}

		// Run the sendFile function to send a file over the stream
		go sendFile(fileStream, "./test/file.txt")

		// Run the writeCounter function to send counter values concurrently
		go writeCounter(fileStream)

		// ... (existing code)
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

func handleFileStream(stream network.Stream) {
	fmt.Println("New incoming file stream from", stream.Conn().RemotePeer())
	// Run the receiveFile function for incoming file streams
	receiveFile(stream, "received_file.txt")
}

func sendFile(s network.Stream, filePath string) {
	file, err := os.Open(filePath)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	// Create a buffer for reading the file in chunks
	buffer := make([]byte, 1024)

	for {
		// Read a chunk from the file
		n, err := file.Read(buffer)
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}

		// Write the chunk to the stream
		_, err = s.Write(buffer[:n])
		if err != nil {
			panic(err)
		}
	}

	// Close the stream when done
	err = s.Close()
	if err != nil {
		fmt.Println("Error closing stream:", err)
	}
}

func receiveFile(s network.Stream, savePath string) {
	file, err := os.Create(savePath)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	// Create a buffer for writing received data to the file
	buffer := make([]byte, 1024)

	for {
		// Read a chunk from the stream
		n, err := s.Read(buffer)
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}

		// Write the chunk to the file
		_, err = file.Write(buffer[:n])
		if err != nil {
			panic(err)
		}
	}

	fmt.Println("File received and saved as", savePath)
}

func handleCounterStream(stream network.Stream) {
	fmt.Println("New incoming counter stream from", stream.Conn().RemotePeer())
	// Run the readCounter function for incoming streams
	readCounter(stream)
}

func writeCounter(s network.Stream) {
	var counter uint64

	for {
		<-time.After(time.Second)
		counter++

		// Write the counter value to the stream
		err := binary.Write(s, binary.BigEndian, counter)
		if err != nil {
			panic(err)
		}
	}
}

func readCounter(s network.Stream) {
	for {
		var counter uint64

		// Read the counter value from the stream
		err := binary.Read(s, binary.BigEndian, &counter)
		if err != nil {
			panic(err)
		}

		fmt.Printf("Received %d from %s\n", counter, s.Conn().RemotePeer())
	}
}
