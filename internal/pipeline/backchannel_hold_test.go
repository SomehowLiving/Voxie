package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// A backchannel that outlasts the 600ms window ("yeah, okay, sure...") used
// to be confirmed as a barge-in on length alone, cutting the agent off for
// words that asked for nothing. Found in a live AssemblyAI call: the agent's
// greeting was stopped by "yeah okay", the acknowledgement turn was then
// dropped as passive, and the caller got dead air. These cover the fix: past
// the window, backchannel-only partial text holds the agent ducked, content
// confirms the barge-in the moment it appears, and a stalled hold is capped.

// scriptedPartial is one partial transcript the fake ASR sends, after a delay
// measured from the moment the client connects.
type scriptedPartial struct {
	after time.Duration
	text  string
}

// fakeScriptedASR is fakeVibeVoiceASR with a timeline of partials, so a test
// can make the caller's words change while they are still talking.
func fakeScriptedASR(t *testing.T, script []scriptedPartial) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			start := time.Now()
			for _, s := range script {
				select {
				case <-time.After(time.Until(start.Add(s.after))):
				case <-ctx.Done():
					return
				}
				msg := `{"text":"` + s.text + `","is_final":false}`
				if conn.WriteMessage(websocket.TextMessage, []byte(msg)) != nil {
					return
				}
			}
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newHoldTestPipeline(t *testing.T, script []scriptedPartial) *Pipeline {
	t.Helper()
	server := fakeScriptedASR(t, script)
	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bar := true
	cfg.Pipeline.BargeIn = &bar
	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)
	return p
}

func TestIsBackchannelTranscript(t *testing.T) {
	cases := map[string]bool{
		"yeah okay":                 true,
		"Yeah, okay. Sure.":         true,
		"mm-hm right":               true,
		"":                          false,
		"okay stop":                 false,
		"yeah okay but i never did": false,
		"the billing question":      false,
	}
	for in, want := range cases {
		if got := isBackchannelTranscript(in); got != want {
			t.Errorf("isBackchannelTranscript(%q) = %v, want %v", in, got, want)
		}
	}
}

// "Yeah, okay, sure" held for over a second: the agent ducks but is never cut
// off, and comes back to full volume when the caller stops.
func TestInboundLongBackchannelHoldsDuckInsteadOfInterrupting(t *testing.T) {
	p := newHoldTestPipeline(t, []scriptedPartial{
		{0, "yeah"},
		{500 * time.Millisecond, "yeah okay"},
		{900 * time.Millisecond, "yeah okay sure"},
	})

	// ~1.2s of speech: twice the backchannel window.
	pushPaced(t, p, loudSamples(), 120, 10*time.Millisecond)
	if interruptFired(p) {
		t.Fatal("a backchannel longer than 600ms cut the agent off, want it held")
	}
	if !p.audioMuted.Load() {
		t.Fatal("agent not ducked while the caller is talking over it")
	}

	pushPaced(t, p, silentSamples(), 40, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if interruptFired(p) {
		t.Error("the held backchannel fired a barge-in when it ended, want it suppressed")
	}
	if p.audioMuted.Load() {
		t.Error("agent still ducked after the backchannel ended, want its volume restored")
	}
}

// The same opening, but the caller goes on to say something: the barge-in is
// confirmed as soon as the partial text carries content, not held to the cap.
func TestInboundHeldBackchannelConfirmsWhenContentArrives(t *testing.T) {
	p := newHoldTestPipeline(t, []scriptedPartial{
		{0, "yeah okay"},
		{1000 * time.Millisecond, "yeah okay but i never authorized that"},
	})

	start := time.Now()
	pushPaced(t, p, loudSamples(), 80, 10*time.Millisecond)
	if interruptFired(p) {
		t.Fatal("barge-in fired while the partial text was still only a backchannel")
	}
	pushUntilInterrupt(t, p, loudSamples(), 10*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("barge-in took %v after content arrived at ~1s, want it confirmed promptly rather than at the 3s cap", took)
	}
}

// Partials that stall on "yeah" while the caller keeps talking cannot hold the
// agent forever: past the cap, sustained speech is a barge-in again.
func TestInboundBackchannelHoldIsCapped(t *testing.T) {
	p := newHoldTestPipeline(t, []scriptedPartial{{0, "yeah"}})

	start := time.Now()
	pushUntilInterrupt(t, p, loudSamples(), 10*time.Millisecond)
	if took := time.Since(start); took < 2500*time.Millisecond {
		t.Errorf("barge-in fired after %v, want the backchannel held until the ~3s cap", took)
	}
}

// Safety net for the dead air: if the greeting is cut off anyway, the text the
// caller interrupted must be known, so their "yeah okay" is answered rather
// than dropped as a passive acknowledgement.
func TestGreetingIsRecordedAsTheAgentsCurrentText(t *testing.T) {
	p, cancel := newLifecyclePipeline(t, nil, nil)
	defer cancel()
	p.ttsClient = &fakeTTS{}

	p.greet("Hi Arjun, this is REX calling about your payment.")

	if got, _ := p.lastAgentText.Load().(string); got != "Hi Arjun, this is REX calling about your payment." {
		t.Fatalf("lastAgentText after greeting = %q, want the greeting text", got)
	}

	// What runAgent does on a barge-in, then the acknowledgement turn.
	p.interruptedText.Store(p.lastAgentText.Load())
	ack := TranscriptEvent{Text: "yeah okay", Final: true, OverAgentSpeech: true}
	if p.isPassiveAcknowledgement(ack) {
		t.Error("an acknowledgement that interrupted the greeting was ignored, want it answered")
	}
}
