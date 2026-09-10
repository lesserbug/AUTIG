package network

import (
	"net"
	"strings"
	"testing"
	"time"
)

// Bind ephemeral ports before starting any dialers. Install handlers before
// starting workers so the fixture does not rely on concurrent Register calls.
func testNetworks(t *testing.T, count int) ([]*DistributedNetwork, []chan Message) {
	t.Helper()
	addresses := make(map[uint64]string, count)
	listeners := make([]net.Listener, count)
	for i := range listeners {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		listeners[i] = listener
		addresses[uint64(i)] = listener.Addr().String()
	}
	nodes := make([]*DistributedNetwork, count)
	received := make([]chan Message, count)
	for i := range nodes {
		inbox := make(chan Message, count*count)
		received[i] = inbox
		n := &DistributedNetwork{
			config:       NetworkConfig{ReplicaID: uint64(i), ReplicaAddr: addresses},
			handler:      func(msg Message) { inbox <- msg },
			connections:  make(map[uint64]*peerConn),
			listener:     listeners[i],
			stopChan:     make(chan struct{}),
			msgQueue:     make(chan Message, 100),
			connAttempts: make(map[uint64]bool),
			readyChan:    make(chan struct{}),
		}
		nodes[i] = n
		n.wg.Add(3)
		go n.messageWorker()
		go n.acceptConnections()
		go n.connectionManager()
		t.Cleanup(n.Stop)
	}
	return nodes, received
}

func testPeer(n *DistributedNetwork, id uint64) *peerConn {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.connections[id]
}

func TestOnlyLowerReplicaDials(t *testing.T) {
	nodes, _ := testNetworks(t, 2)
	done := make(chan struct{})
	go func() {
		nodes[1].tryConnect(0, nodes[0].listener.Addr().String())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("higher replica attempted an outgoing connection to lower replica")
	}
	if testPeer(nodes[1], 0) != nil || testPeer(nodes[0], 1) != nil {
		t.Fatal("higher replica established a connection")
	}
}

func TestFiveNodeMeshAndReconnect(t *testing.T) {
	nodes, received := testNetworks(t, 5)
	for _, n := range nodes {
		if err := n.WaitForPeers(10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	assertDelivery := func() {
		t.Helper()
		for from, n := range nodes {
			for to := range nodes {
				if from != to && !n.Send(Message{Type: "test", From: uint64(from), To: uint64(to), Payload: "bidirectional"}) {
					t.Fatalf("send %d -> %d failed", from, to)
				}
			}
		}
		for to, inbox := range received {
			seen := make(map[uint64]bool)
			deadline := time.NewTimer(5 * time.Second)
			for len(seen) < len(nodes)-1 {
				select {
				case msg := <-inbox:
					if msg.To != uint64(to) || msg.From == uint64(to) || seen[msg.From] || msg.Payload != "bidirectional" {
						t.Fatalf("unexpected message at node %d: %+v", to, msg)
					}
					seen[msg.From] = true
				case <-deadline.C:
					t.Fatalf("node %d received only %d/%d messages", to, len(seen), len(nodes)-1)
				}
			}
			deadline.Stop()
		}
	}
	assertDelivery()

	// Verify both endpoints reference the same stream, owned by the lower ID.
	for low := range nodes {
		for high := low + 1; high < len(nodes); high++ {
			out, in := testPeer(nodes[low], uint64(high)), testPeer(nodes[high], uint64(low))
			if out == nil || in == nil {
				t.Fatalf("missing pair %d/%d", low, high)
			}
			if out.conn.RemoteAddr().String() != nodes[high].listener.Addr().String() ||
				out.conn.LocalAddr().String() != in.conn.RemoteAddr().String() {
				t.Fatalf("pair %d/%d does not share a lower-ID-dialed stream", low, high)
			}
		}
	}

	// Close the accepting endpoint of pair 1/2. The lower-ID node must redial
	// even though the one-shot startup readiness signal has already fired.
	oldOut, oldIn := testPeer(nodes[1], 2), testPeer(nodes[2], 1)
	if err := oldIn.conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		out, in := testPeer(nodes[1], 2), testPeer(nodes[2], 1)
		if out != nil && in != nil && out != oldOut && in != oldIn {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pair 1/2 did not reconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertDelivery()
}

func TestWaitForPeersReportsMissingIDs(t *testing.T) {
	n := &DistributedNetwork{
		config: NetworkConfig{ReplicaID: 1, ReplicaAddr: map[uint64]string{
			0: "unused", 1: "unused", 2: "unused", 3: "unused", 4: "unused",
		}},
		connections: map[uint64]*peerConn{4: {}, 0: {}, 3: {}},
		readyChan:   make(chan struct{}),
	}
	err := n.WaitForPeers(time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "Connected to 3/4; connected peers=[0 3 4]; missing peers=[2]") {
		t.Fatalf("unexpected timeout diagnostic: %v", err)
	}
}
