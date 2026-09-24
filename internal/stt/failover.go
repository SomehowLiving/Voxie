package stt

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// A failover STT client runs one provider at a time from an ordered list
// ([stt] failover = ["sarvam", "deepgram"]). It starts with the first one
// that connects; when the active provider's connection breaks mid-call
// (SendAudio returns an error), it closes it, starts the next one and
// carries on -- the call keeps its ears instead of ending. Only when every
// provider has failed does the error reach the pipeline, which is what a
// single provider's failure did before.
//
// Providers differ in what they're good at (Sarvam: Indian languages;
// Deepgram: world languages), so a deployment orders the list for the
// callers it serves.
//
// Two ways a provider fails:
//   - loudly: SendAudio errors (the connection dropped);
//   - silently: it keeps accepting audio but stops returning transcripts.
//     Seen live: Deepgram went quiet for 73s before its socket finally
//     broke. So if the caller clearly spoke and nothing came back within
//     stallTimeout of them stopping, the provider is treated as failed.
// Either way the client moves to the next provider, wrapping round to
// reconnect the one that failed before giving up -- a fresh connection is
// often all a transient drop needs.

// speechPeak is the sample peak above which a frame counts as the caller
// speaking (for stall detection only; it doesn't gate any audio), and
// speechFrames how many such frames it takes (~0.5s of audio) before the
// watchdog expects a transcript -- a cough or a bump yields none, rightly.
const (
	speechPeak   = 2000
	speechFrames = 25
)

// defaultStallTimeout: normal providers return a transcript 0.2-1.5s after
// the caller stops; ten seconds of nothing means the provider has stopped
// listening.
const defaultStallTimeout = 10 * time.Second

type providerFactory func(ctx context.Context, name string, onResult func(TranscriptResult)) (Client, error)

type failoverClient struct {
	ctx      context.Context
	names    []string
	build    providerFactory
	onResult func(TranscriptResult)
	partials bool

	mu      sync.Mutex
	current Client
	index   int // position of current in names
	// activePartials mirrors whether the provider serving now streams
	// partials; read on every barge-in decision, so it's lock-free.
	activePartials atomic.Bool

	stallTimeout  time.Duration
	speechPending bool      // the caller spoke after the last transcript
	loudFrames    int       // loud frames since the last transcript
	lastSpeechAt  time.Time // last loud frame
}

func newFailoverClient(ctx context.Context, names []string, build providerFactory, partials bool, onResult func(TranscriptResult)) (Client, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("stt provider \"failover\" requires [stt] failover = [\"provider\", ...]")
	}
	f := &failoverClient{ctx: ctx, names: names, build: build, partials: partials, index: -1, stallTimeout: defaultStallTimeout}
	f.onResult = func(r TranscriptResult) {
		f.mu.Lock()
		f.speechPending, f.loudFrames = false, 0
		if f.index >= 0 {
			r.Provider = f.names[f.index]
		}
		f.mu.Unlock()
		if onResult != nil {
			onResult(r)
		}
	}
	if err := f.startFrom(0, len(names)); err != nil {
		return nil, err
	}
	return f, nil
}

// startFrom tries up to `attempts` providers in list order starting at
// position `from`, wrapping round the list, and keeps the first that
// connects. Caller holds mu (or is the constructor).
func (f *failoverClient) startFrom(from, attempts int) error {
	var errs []error
	for k := 0; k < attempts; k++ {
		i := (from + k) % len(f.names)
		name := f.names[i]
		c, err := f.build(f.ctx, name, f.onResult)
		if err != nil {
			log.Printf("[stt:failover] %s unavailable: %v", name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if f.index >= 0 && f.index == i {
			log.Printf("[stt:failover] reconnected %s", name)
		} else if f.index >= 0 {
			log.Printf("[stt:failover] switched from %s to %s", f.names[f.index], name)
		} else if i > 0 {
			log.Printf("[stt:failover] started on %s (earlier providers unavailable)", name)
		}
		f.current, f.index = c, i
		f.activePartials.Store(f.partials && providerEmitsPartials(name))
		return nil
	}
	return fmt.Errorf("stt failover: no provider available: %w", errors.Join(errs...))
}

func (f *failoverClient) SendAudio(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current == nil {
		return fmt.Errorf("stt failover: no active provider")
	}
	now := time.Now()
	if framePeak(data) > speechPeak {
		f.loudFrames++
		f.lastSpeechAt = now
		if f.loudFrames >= speechFrames {
			f.speechPending = true
		}
	}
	err := f.current.SendAudio(data)
	if err == nil && f.speechPending && now.Sub(f.lastSpeechAt) > f.stallTimeout {
		err = fmt.Errorf("no transcript %s after the caller spoke", f.stallTimeout)
	}
	if err == nil {
		return nil
	}
	if f.ctx.Err() != nil {
		return err
	}
	failed, failedName := f.current, f.names[f.index]
	log.Printf("[stt:failover] %s failed mid-call: %v", failedName, err)
	go failed.Close()
	f.current = nil
	f.speechPending, f.loudFrames = false, 0
	// Every other provider in order, then the failed one again.
	if startErr := f.startFrom(f.index+1, len(f.names)); startErr != nil {
		return fmt.Errorf("%s failed (%v) and %w", failedName, err, startErr)
	}
	// The frame that exposed the failure goes to the new provider; losing
	// 20ms is harmless but there's no reason to.
	_ = f.current.SendAudio(data)
	return nil
}

// EmitsPartials answers for the provider serving right now. The pipeline
// asks on every barge-in decision, so after a switch to a finals-only
// provider (Sarvam) barge-in moves to its VAD-only path at once, and back
// again when a partials provider takes over. (It used to answer once for
// the whole chain, which put a Deepgram-first chain with Sarvam as its
// backup on VAD-only barge-in for the whole call.)
func (f *failoverClient) EmitsPartials() bool { return f.activePartials.Load() }

func (f *failoverClient) Close() {
	f.mu.Lock()
	c := f.current
	f.current = nil
	f.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// framePeak returns the largest absolute sample in a linear16 frame.
func framePeak(pcm []byte) int {
	peak := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(pcm[i:])))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return peak
}

// providerEmitsPartials answers PartialsEmitter for a provider by name
// without connecting to it. Only providers known to stream interim text
// say yes; "no" is always safe, since barge-in then runs on VAD alone.
func providerEmitsPartials(name string) bool {
	switch name {
	case "deepgram", "assemblyai":
		return true
	default:
		return false
	}
}
