package session

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/peer"
	"github.com/streamcoreai/streamcore-server/internal/pipeline"
	"github.com/streamcoreai/streamcore-server/internal/plugin"
	"github.com/streamcoreai/streamcore-server/internal/rag"
)

type Session struct {
	ID        string
	cfg       *config.Config
	pluginMgr *plugin.Manager
	ragClient rag.Client
	ctx       context.Context
	cancel    context.CancelFunc

	mu    sync.Mutex
	peers map[string]*peer.Peer
	// idleSince is when the session last dropped to zero peers, or the zero
	// time while it has any. The Manager reaps sessions that stay idle past
	// the grace period — without it, a client that vanishes without sending
	// DELETE (which is exactly what a dropped connection is) leaves an empty
	// Session in the map forever.
	idleSince time.Time
	// etag identifies the current ICE session (RFC 9725 §4.3.1). It rotates on
	// every successful ICE restart so a client racing two restarts can tell
	// which generation it is patching.
	etag string

	// conv is the durable half of the call — LLM history, transcript log,
	// rolling summary. It is built on the first peer and handed to every
	// Pipeline afterwards, so a caller who drops and resumes keeps talking to
	// an agent that remembers them.
	conv *pipeline.ConversationState

	// resourceID is who is on the call, from a verified JWT claim or a trusted
	// server-side caller. Empty when the deployment offers no identity. An
	// external agent uses it to scope memory to the person; the session ID only
	// covers this one call.
	resourceID string

	// buildPipeline constructs the pipeline that runs on a peer once its
	// remote audio track arrives. Injected (defaulting to buildWebRTCPipeline)
	// so Session never calls a pipeline constructor directly — the transport
	// layer owns how a pipeline is built.
	buildPipeline PipelineFactory
}

// SetResourceID binds an identity to the session once. Later calls are ignored
// so a redial cannot hand an in-flight conversation to someone else.
func (s *Session) SetResourceID(resourceID string) {
	if resourceID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resourceID == "" {
		s.resourceID = resourceID
	}
}

func (s *Session) ResourceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resourceID
}

func NewSession(id string, cfg *config.Config, pluginMgr *plugin.Manager, ragClient rag.Client) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:        id,
		cfg:       cfg,
		pluginMgr: pluginMgr,
		ragClient: ragClient,
		ctx:       ctx,
		cancel:    cancel,
		peers:     make(map[string]*peer.Peer),
		// A session that is created and never gets a peer — a POST that fails
		// between GetOrCreate and AddPeer — is idle from birth.
		idleSince: time.Now(),
		etag:      `"` + id + `"`,
	}
	s.buildPipeline = s.buildWebRTCPipeline
	return s
}

// buildWebRTCPipeline is the default PipelineFactory: it builds a WebRTC
// pipeline on the peer's tracks once the remote audio track arrives.
func (s *Session) buildWebRTCPipeline(
	ctx context.Context,
	p *peer.Peer,
	remoteTrack *webrtc.TrackRemote,
	opts PeerOptions,
	conv *pipeline.ConversationState,
	resumed bool,
) (*pipeline.Pipeline, error) {
	return pipeline.New(ctx, s.cfg, remoteTrack, p.LocalTrack(), p.SendEvent, s.pluginMgr, s.ragClient, opts, conv, resumed)
}

// AddPeer creates a new Pion peer and launches a goroutine that waits for
// the remote audio track to arrive, then builds and starts the channel-based
// pipeline. Event messages are delivered via the peer's DataChannel.
// PeerOptions is re-exported so the signalling layer, which already depends on
// this package, does not need to reach into pipeline for it.
type PeerOptions = pipeline.PeerOptions

// PipelineFactory builds the pipeline that runs on a peer once its remote
// audio track arrives. Session holds one so it never calls a pipeline
// constructor directly — construction is the transport layer's concern.
type PipelineFactory func(
	ctx context.Context,
	p *peer.Peer,
	remoteTrack *webrtc.TrackRemote,
	opts PeerOptions,
	conv *pipeline.ConversationState,
	resumed bool,
) (*pipeline.Pipeline, error)

func (s *Session) AddPeer(peerID string, opts PeerOptions) (*peer.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	turnCfg := peer.TURNConfig{
		PublicIP: s.cfg.Server.PublicIP,
		Secret:   s.cfg.Server.TurnSecret,
	}
	p, err := peer.New(s.ctx, peerID, s.cfg.Server.PublicIP, turnCfg)
	if err != nil {
		return nil, err
	}

	// A second peer on this session means a resume after a dropped
	// connection: the conversation already exists and this peer inherits it,
	// which is what carries the history and the rolling summary across the
	// gap. Building it is deferred to the track goroutine so a provider
	// misconfiguration still surfaces where it always has — when audio
	// arrives — rather than turning every POST into a 500.
	resumed := s.conv != nil

	p.OnClose = func() {
		s.removePeer(peerID)
	}

	s.peers[peerID] = p
	s.idleSince = time.Time{}

	// Wait for the remote track to arrive, then start the pipeline.
	go func() {
		// A panic anywhere in this session's call path must cost this call,
		// not the process and every other live call. The pipeline's own
		// goroutines recover themselves and cancel the pipeline; this guard
		// covers the setup below, and the peer teardown after it runs in every
		// exit path so a panicked call is reaped like any other ended one.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[session:%s] panic in peer goroutine: %v\n%s", s.ID, r, debug.Stack())
			}
			p.Close()
		}()

		var pl *pipeline.Pipeline

		select {
		case remoteTrack := <-p.RemoteTrackCh:
			log.Printf("[session:%s] remote track ready, starting pipeline (resumed=%v)", s.ID, resumed)

			conv, err := s.conversation()
			if err != nil {
				log.Printf("[session:%s] conversation state error: %v", s.ID, err)
				p.Close()
				return
			}

			pl, err = s.buildPipeline(p.Context(), p, remoteTrack, opts, conv, resumed)
			if err != nil {
				log.Printf("[session:%s] pipeline create error: %v", s.ID, err)
				p.Close()
				return
			}

			// Route incoming data channel messages to the pipeline (e.g. image chunks).
			p.OnDataChannelMessage = func(msg string) {
				pl.HandleDataChannelMessage(msg)
			}
		case <-s.ctx.Done():
			return
		}

		// Start blocks until the pipeline context is cancelled.
		pl.Start()
	}()

	return p, nil
}

// RunMedia runs a call over an audio link that isn't a WebRTC peer -- a
// phone stream such as Twilio Media Streams -- and blocks until the call
// ends: ctx is cancelled (the caller hung up), the session closes, or the
// pipeline finishes (the agent ended the call).
func (s *Session) RunMedia(ctx context.Context, media pipeline.Media, opts PeerOptions) (err error) {
	s.mu.Lock()
	s.idleSince = time.Time{} // in use: not the reaper's to collect
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.idleSince = time.Now()
		s.mu.Unlock()
		// As in AddPeer: a panic costs this call, not the process.
		if r := recover(); r != nil {
			log.Printf("[session:%s] panic in media call: %v\n%s", s.ID, r, debug.Stack())
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	conv, err := s.conversation()
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-callCtx.Done():
		}
	}()
	// No DataChannel on a phone line: transcript and state events have
	// nowhere to go, which the pipeline treats as a client that ignores them.
	noEvents := func(interface{}) error { return nil }
	pl, err := pipeline.NewWithMedia(callCtx, s.cfg, media, noEvents, s.pluginMgr, s.ragClient, opts, conv, false)
	if err != nil {
		return err
	}
	pl.Start()
	return nil
}

// conversation returns the session's durable state, building it on first use.
// Every Pipeline this session ever starts shares the one instance — that
// sharing is what a resume preserves.
func (s *Session) conversation() (*pipeline.ConversationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conv == nil {
		// Read the field directly, not ResourceID() — the mutex is already held.
		conv, err := pipeline.NewConversationState(s.cfg, s.ID, s.resourceID)
		if err != nil {
			return nil, err
		}
		s.conv = conv
	}
	return s.conv, nil
}

func (s *Session) GetPeer(peerID string) *peer.Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers[peerID]
}

func (s *Session) removePeer(peerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, peerID)
	if len(s.peers) == 0 {
		s.idleSince = time.Now()
	}
	log.Printf("[session:%s] peer %s removed, %d peers remaining", s.ID, peerID, len(s.peers))
}

func (s *Session) PeerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers)
}

// IdleSince returns when the session last dropped to zero peers, or the zero
// time if it still has any. The grace period it starts is what keeps the door
// open for an ICE restart or a client redial after a transport drop.
func (s *Session) IdleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleSince
}

// ETag returns the current ICE-session ETag, quoted and ready for the header.
func (s *Session) ETag() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.etag
}

// RotateETag issues a new ICE-session ETag and returns it. Called after a
// successful ICE restart: the previous tag now refers to a generation that no
// longer exists, so a PATCH still carrying it is stale.
func (s *Session) RotateETag() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.etag = `"` + uuid.NewString() + `"`
	return s.etag
}

// Resumable reports whether reattaching would actually preserve a
// conversation. A realtime (speech-to-speech) session is not: its history
// lives inside the provider, and a new provider connection cannot inherit it,
// so offering a resume token would promise continuity the server cannot keep.
//
// Decided from configuration rather than from the built state, so the answer
// is available at POST time — before any audio has arrived to build it.
func (s *Session) Resumable() bool {
	return s.cfg != nil && !s.cfg.RealtimeEnabled()
}

// evictPeers closes every peer currently attached, so a resuming client does
// not end up sharing the session with the corpse of the connection it lost.
func (s *Session) evictPeers() {
	// p.Close() calls OnClose → removePeer, which takes s.mu, so the map is
	// drained before anything is closed.
	s.mu.Lock()
	peers := make([]*peer.Peer, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.peers = make(map[string]*peer.Peer)
	s.mu.Unlock()

	for _, p := range peers {
		p.Close()
	}
}

func (s *Session) Close() {
	s.cancel()

	// Collect peers and clear the map before closing them.
	// p.Close() calls p.OnClose → s.removePeer which locks s.mu,
	// so we must NOT hold s.mu while closing peers.
	s.mu.Lock()
	peers := make([]*peer.Peer, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.peers = make(map[string]*peer.Peer)
	s.mu.Unlock()

	for _, p := range peers {
		p.Close()
	}
	log.Printf("[session:%s] closed", s.ID)
}
