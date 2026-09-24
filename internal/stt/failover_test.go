package stt

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeProvider records audio and can be made to fail.
type fakeProvider struct {
	name   string
	mu     sync.Mutex
	frames int
	failOn int // SendAudio call number that starts failing (0 = never)
	closed bool
}

func (p *fakeProvider) SendAudio([]byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames++
	if p.failOn > 0 && p.frames >= p.failOn {
		return errors.New("connection lost")
	}
	return nil
}

func (p *fakeProvider) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

func factory(providers map[string]*fakeProvider, unavailable map[string]bool) providerFactory {
	return func(_ context.Context, name string, _ func(TranscriptResult)) (Client, error) {
		if unavailable[name] {
			return nil, errors.New("dial refused")
		}
		return providers[name], nil
	}
}

func TestFailoverStartsOnTheFirstProviderThatConnects(t *testing.T) {
	providers := map[string]*fakeProvider{"sarvam": {name: "sarvam"}, "deepgram": {name: "deepgram"}}
	c, err := newFailoverClient(context.Background(), []string{"sarvam", "deepgram"}, factory(providers, map[string]bool{"sarvam": true}), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SendAudio(make([]byte, 640))
	if providers["deepgram"].frames != 1 {
		t.Errorf("audio should reach deepgram when sarvam can't connect")
	}
}

func TestFailoverSwitchesMidCallAndKeepsTheFrame(t *testing.T) {
	providers := map[string]*fakeProvider{"sarvam": {name: "sarvam", failOn: 3}, "deepgram": {name: "deepgram"}}
	c, err := newFailoverClient(context.Background(), []string{"sarvam", "deepgram"}, factory(providers, nil), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := c.SendAudio(make([]byte, 640)); err != nil {
			t.Fatalf("frame %d: %v -- a failover with a healthy backup must not surface the error", i, err)
		}
	}
	if providers["deepgram"].frames != 3 {
		t.Errorf("deepgram got %d frames, want 3 (the frame that exposed the failure, then 2 more)", providers["deepgram"].frames)
	}
}

func TestFailoverErrorsOnlyWhenEveryProviderHasFailed(t *testing.T) {
	providers := map[string]*fakeProvider{"sarvam": {name: "sarvam", failOn: 1}, "deepgram": {name: "deepgram", failOn: 1}}
	// Each provider connects once; once it has dropped, reconnecting fails
	// too -- every provider really is down.
	built := map[string]bool{}
	build := func(ctx context.Context, name string, onResult func(TranscriptResult)) (Client, error) {
		if built[name] {
			return nil, errors.New("still down")
		}
		built[name] = true
		return factory(providers, nil)(ctx, name, onResult)
	}
	c, err := newFailoverClient(context.Background(), []string{"sarvam", "deepgram"}, build, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendAudio(make([]byte, 640)); err != nil {
		t.Fatalf("first failure should switch to deepgram, got %v", err)
	}
	if err := c.SendAudio(make([]byte, 640)); err == nil {
		t.Fatal("with every provider failed, the error must reach the pipeline")
	}
	if _, err := newFailoverClient(context.Background(), []string{"sarvam"}, factory(providers, map[string]bool{"sarvam": true}), false, nil); err == nil {
		t.Fatal("no provider connecting must fail construction")
	}
}

func TestFailoverPartialsAreConservative(t *testing.T) {
	if providerEmitsPartials("sarvam") || !providerEmitsPartials("deepgram") {
		t.Fatal("sarvam is finals-only, deepgram streams partials")
	}
}

// loudFrame is 20ms of speech-level audio; quietFrame is silence.
func loudFrame() []byte {
	b := make([]byte, 640)
	for i := 0; i < len(b); i += 2 {
		binary.LittleEndian.PutUint16(b[i:], uint16(int16(8000)))
	}
	return b
}

func quietFrame() []byte { return make([]byte, 640) }

// A provider that keeps accepting audio but never answers is treated as
// failed once the caller has spoken and stallTimeout passes -- and the
// client reconnects (here: the same provider, the only one configured).
func TestFailoverTreatsASilentProviderAsFailedAndReconnects(t *testing.T) {
	builds := 0
	build := func(_ context.Context, name string, _ func(TranscriptResult)) (Client, error) {
		builds++
		return &fakeProvider{name: name}, nil
	}
	c, err := newFailoverClient(context.Background(), []string{"deepgram"}, build, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := c.(*failoverClient)
	f.stallTimeout = 50 * time.Millisecond
	for i := 0; i < 30; i++ { // 0.6s of speech
		_ = c.SendAudio(loudFrame())
	}
	time.Sleep(80 * time.Millisecond)
	if err := c.SendAudio(quietFrame()); err != nil {
		t.Fatalf("a reconnect should succeed, got %v", err)
	}
	if builds != 2 {
		t.Fatalf("built the provider %d times, want a reconnect (2)", builds)
	}
}

// A cough: a short loud burst with no words. No transcript is expected, so
// it must not trip the watchdog.
func TestFailoverIgnoresAShortBurst(t *testing.T) {
	builds := 0
	build := func(_ context.Context, name string, _ func(TranscriptResult)) (Client, error) {
		builds++
		return &fakeProvider{name: name}, nil
	}
	c, _ := newFailoverClient(context.Background(), []string{"deepgram"}, build, true, nil)
	c.(*failoverClient).stallTimeout = 50 * time.Millisecond
	for i := 0; i < 5; i++ { // 100ms
		_ = c.SendAudio(loudFrame())
	}
	time.Sleep(80 * time.Millisecond)
	_ = c.SendAudio(quietFrame())
	if builds != 1 {
		t.Fatalf("a short burst caused %d reconnects", builds-1)
	}
}

// A transcript resets the watchdog: a working provider is never replaced.
func TestFailoverKeepsAProviderThatAnswers(t *testing.T) {
	var deliver func(TranscriptResult)
	builds := 0
	build := func(_ context.Context, name string, onResult func(TranscriptResult)) (Client, error) {
		builds++
		deliver = onResult
		return &fakeProvider{name: name}, nil
	}
	c, _ := newFailoverClient(context.Background(), []string{"deepgram"}, build, true, nil)
	c.(*failoverClient).stallTimeout = 50 * time.Millisecond
	for i := 0; i < 30; i++ {
		_ = c.SendAudio(loudFrame())
	}
	deliver(TranscriptResult{Text: "hello", IsFinal: true})
	time.Sleep(80 * time.Millisecond)
	_ = c.SendAudio(quietFrame())
	if builds != 1 {
		t.Fatalf("a provider that answered was replaced")
	}
}

// When every other provider is down, the one that dropped is reconnected
// rather than the call going deaf (the live drill: Sarvam's key rejected,
// then Deepgram's socket broke).
func TestFailoverWrapsRoundToReconnectTheFailedProvider(t *testing.T) {
	deepgramBuilds := 0
	build := func(_ context.Context, name string, _ func(TranscriptResult)) (Client, error) {
		if name == "sarvam" {
			return nil, errors.New("bad handshake")
		}
		deepgramBuilds++
		if deepgramBuilds == 1 {
			return &fakeProvider{name: name, failOn: 2}, nil
		}
		return &fakeProvider{name: name}, nil
	}
	c, err := newFailoverClient(context.Background(), []string{"sarvam", "deepgram"}, build, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := c.SendAudio(quietFrame()); err != nil {
			t.Fatalf("frame %d: %v -- deepgram should have been reconnected", i, err)
		}
	}
	if deepgramBuilds != 2 {
		t.Fatalf("deepgram built %d times, want 2 (original + reconnect)", deepgramBuilds)
	}
}
