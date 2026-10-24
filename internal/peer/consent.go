package peer

import (
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v2"
	"github.com/pion/sdp/v3"
	"github.com/pion/stun"
	"github.com/pion/transport/v2"
	"github.com/pion/transport/v2/stdnet"
	"github.com/pion/webrtc/v3"
)

// consentInterval is the mean time between two consent checks of a
// transport (RFC 7675: 5 s, randomized by ±20%). A variable for tests.
var consentInterval = 5 * time.Second

// consentResponseTTL is how long an answer to a consent check is expected.
const consentResponseTTL = 10 * time.Second

// consent sends RFC 7675 consent freshness checks on a PeerConnection's
// selected candidate pair, as browsers do (010 T016): a Binding request
// every consentInterval, whether or not media flows. Pion only sends its
// keepalive when the pair is idle, so media servers that enforce consent
// (some after 20 s without a check) would close a busy transport.
//
// The checks leave from the pair's own socket: consent opens the sockets
// through consentNet, which also keeps the answers to the checks away from
// Pion (it would log them as unknown transactions).
type consent struct {
	pc       *webrtc.PeerConnection
	logger   *slog.Logger
	net      *consentNet
	interval time.Duration
}

func newConsent(logger *slog.Logger) (*consent, error) {
	inner, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	return &consent{logger: logger, interval: consentInterval, net: &consentNet{Net: inner, sent: map[[stun.TransactionIDSize]byte]time.Time{}}}, nil
}

// start runs the checks of a newly created PeerConnection.
func (c *consent) start(pc *webrtc.PeerConnection, err error) (*webrtc.PeerConnection, error) {
	if err != nil {
		return nil, err
	}
	c.pc = pc
	go c.run()
	return pc, nil
}

// run sends the checks until the PeerConnection closes.
func (c *consent) run() {
	for {
		jitter := 0.8 + 0.4*rand.Float64()
		time.Sleep(time.Duration(float64(c.interval) * jitter))
		if c.pc.ConnectionState() == webrtc.PeerConnectionStateClosed {
			return
		}
		if err := c.check(); err != nil {
			c.logger.Debug("Consent check not sent", slog.String("error", err.Error()))
		}
	}
}

// check sends one Binding request on the selected pair, if there is one.
func (c *consent) check() error {
	sctp := c.pc.SCTP()
	if sctp == nil || sctp.Transport() == nil {
		return nil
	}
	it := sctp.Transport().ICETransport()
	pair, err := it.GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return nil // Not connected yet
	}
	local, err := it.GetLocalParameters()
	if err != nil {
		return err
	}
	rd := c.pc.RemoteDescription()
	if rd == nil {
		return nil
	}
	ufrag, pwd := remoteICECredentials(rd.SDP)
	if ufrag == "" || pwd == "" {
		return nil
	}
	conn := c.net.conn(pair.Local.Port)
	if conn == nil {
		return nil // Not a socket of ours (e.g. a TCP candidate)
	}
	role := stun.Setter(ice.AttrControlled(rand.Uint64()))
	if it.Role() == webrtc.ICERoleControlling {
		role = ice.AttrControlling(rand.Uint64())
	}
	msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
		stun.NewUsername(ufrag+":"+local.UsernameFragment),
		role,
		ice.PriorityAttr(pair.Local.Priority),
		stun.NewShortTermIntegrity(pwd),
		stun.Fingerprint,
	)
	if err != nil {
		return err
	}
	c.net.expect(msg.TransactionID)
	_, err = conn.WriteTo(msg.Raw, &net.UDPAddr{IP: net.ParseIP(pair.Remote.Address), Port: int(pair.Remote.Port)})
	return err
}

// remoteICECredentials reads the remote ICE username fragment and password
// from an SDP, at session or first media level.
func remoteICECredentials(s string) (ufrag, pwd string) {
	var d sdp.SessionDescription
	if err := d.Unmarshal([]byte(s)); err != nil {
		return "", ""
	}
	ufrag, _ = d.Attribute("ice-ufrag")
	pwd, _ = d.Attribute("ice-pwd")
	for _, md := range d.MediaDescriptions {
		if ufrag != "" && pwd != "" {
			break
		}
		if v, ok := md.Attribute("ice-ufrag"); ok && ufrag == "" {
			ufrag = v
		}
		if v, ok := md.Attribute("ice-pwd"); ok && pwd == "" {
			pwd = v
		}
	}
	return strings.TrimSpace(ufrag), strings.TrimSpace(pwd)
}

// consentNet is the network Pion opens its UDP sockets on: the standard
// one, with each socket recorded and its reads filtered of the answers to
// consent checks.
type consentNet struct {
	transport.Net

	mu    sync.Mutex
	conns []*consentConn
	sent  map[[stun.TransactionIDSize]byte]time.Time // Checks awaiting an answer
}

func (n *consentNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	cc := &consentConn{UDPConn: c, n: n}
	n.mu.Lock()
	n.conns = append(n.conns, cc)
	n.mu.Unlock()
	return cc, nil
}

// conn returns the socket bound to port.
func (n *consentNet) conn(port uint16) *consentConn {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.conns {
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.Port == int(port) {
			return c
		}
	}
	return nil
}

// expect records a sent check; old ones are forgotten.
func (n *consentNet) expect(id [stun.TransactionIDSize]byte) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	for k, at := range n.sent {
		if now.Sub(at) > consentResponseTTL {
			delete(n.sent, k)
		}
	}
	n.sent[id] = now
}

// answered reports whether pkt answers a consent check, and forgets it.
func (n *consentNet) answered(pkt []byte) bool {
	if !stun.IsMessage(pkt) {
		return false
	}
	var m stun.Message
	m.Raw = append([]byte(nil), pkt...)
	if err := m.Decode(); err != nil || m.Type.Method != stun.MethodBinding || m.Type.Class == stun.ClassRequest {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.sent[m.TransactionID]; !ok {
		return false
	}
	delete(n.sent, m.TransactionID)
	return true
}

// consentConn is a UDP socket whose reads skip the answers to consent
// checks.
type consentConn struct {
	transport.UDPConn
	n *consentNet
}

func (c *consentConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.UDPConn.ReadFrom(p)
		if err != nil || !c.n.answered(p[:n]) {
			return n, addr, err
		}
	}
}

func (c *consentConn) ReadFromUDP(p []byte) (int, *net.UDPAddr, error) {
	for {
		n, addr, err := c.UDPConn.ReadFromUDP(p)
		if err != nil || !c.n.answered(p[:n]) {
			return n, addr, err
		}
	}
}
