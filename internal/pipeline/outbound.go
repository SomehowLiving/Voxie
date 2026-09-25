package pipeline

import (
	"log"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/audio"
)

// runSender reads PCM frames from outPCMCh and writes them to the media link
// (Opus RTP for WebRTC, μ-law for a phone stream) with wall-clock pacing
// (20ms/frame).
func (p *Pipeline) runSender() {
	for {
		// Wait for the first frame of a talkspurt
		var frame PCMFrame
		select {
		case <-p.ctx.Done():
			return
		case frame = <-p.outPCMCh:
		}

		p.streamTalkspurt(frame)
	}
}

// streamTalkspurt sends a continuous run of PCM frames with wall-clock pacing.
// It returns when the channel is empty for >100ms (gap between utterances)
// or the context is cancelled.
func (p *Pipeline) streamTalkspurt(first PCMFrame) {
	if first.NewTalkspurt {
		p.markTalkspurt()
	}
	p.encodeAndSend(first)

	start := time.Now()
	idx := 1

	for {
		target := start.Add(time.Duration(idx) * 20 * time.Millisecond)
		wait := time.Until(target)

		// Use a short timer to detect gaps between TTS utterances.
		// If no frame arrives within 100ms, the talkspurt is over.
		timer := time.NewTimer(100 * time.Millisecond)

		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case frame := <-p.outPCMCh:
			timer.Stop()
			if frame.NewTalkspurt {
				p.markTalkspurt()
				start = time.Now()
				idx = 0
			}
			if wait > 0 {
				time.Sleep(wait)
			}
			p.encodeAndSend(frame)
			idx++
		case <-timer.C:
			// No frames for 100ms — talkspurt ended
			return
		}
	}
}

// encodeAndSend sends a single PCM frame on the media link.
func (p *Pipeline) encodeAndSend(frame PCMFrame) {
	samples := frame.Samples
	if len(samples) < audio.FrameSize {
		padded := make([]int16, audio.FrameSize)
		copy(padded, samples)
		samples = padded
	}

	// While the caller is talking over the agent, attenuate rather than cut.
	// A backchannel then costs a brief dip in volume instead of a clipped
	// word, and the duck lifts as soon as the caller stops.
	if p.audioMuted.Load() {
		ducked := make([]int16, len(samples))
		for i, v := range samples {
			ducked[i] = int16(int32(v) * duckGainNumerator / duckGainDenominator)
		}
		samples = ducked
	}

	// Recorded here rather than at enqueue: this is the signal that actually
	// goes on the wire, duck attenuation and padding included, and it is what
	// can come back as echo on a path with no AEC. No-op when the guard is off.
	p.echoGuard.Observe(samples)

	p.rtpMu.Lock()
	talkspurt := p.markerNext
	p.markerNext = false
	p.rtpMu.Unlock()

	if p.media == nil {
		return
	}
	if err := p.media.WriteFrame(samples, talkspurt); err != nil {
		if p.ctx.Err() == nil {
			log.Printf("[sender] write error: %v", err)
		}
	}
}

// markTalkspurt sets the RTP marker bit for the next packet.
func (p *Pipeline) markTalkspurt() {
	p.rtpMu.Lock()
	defer p.rtpMu.Unlock()
	p.markerNext = true
}

// Duck attenuation, expressed as an integer ratio so the hot path does no
// float conversion. 1/4 is roughly -12 dB: clearly quieter, still audible
// enough that the caller knows the agent is mid-sentence.
const (
	duckGainNumerator   = 1
	duckGainDenominator = 4
)
