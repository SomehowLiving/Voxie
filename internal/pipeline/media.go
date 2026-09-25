package pipeline

import (
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/streamcoreai/streamcore-server/internal/audio"
)

// Media is the pipeline's audio link to the caller: 20ms frames of 16kHz
// mono PCM (audio.FrameSize samples) in each direction. WebRTC (WHIP) is
// one implementation; a phone-network stream, such as Twilio Media
// Streams, is another. Everything past this interface -- hearing,
// turn-taking, barge-in, the voice -- is the same for both.
type Media interface {
	// ReadFrame blocks for the caller's next frame. An error ends the call's
	// inbound audio.
	ReadFrame() ([]int16, error)
	// WriteFrame sends one frame of the agent's voice. The pipeline paces
	// calls at 20ms. talkspurt is true on the first frame of an utterance.
	WriteFrame(samples []int16, talkspurt bool) error
}

// MediaClearer is implemented by media that buffer the agent's audio on the
// far side (Twilio plays what it has been sent). Clear drops that buffer, so
// an interrupted agent stops at once rather than finishing the buffered audio.
type MediaClearer interface {
	Clear()
}

// webrtcMedia is Media over a WHIP peer's tracks: Opus in RTP each way.
type webrtcMedia struct {
	remote  *webrtc.TrackRemote
	local   *webrtc.TrackLocalStaticRTP
	decoder *audio.OpusDecoder
	encoder *audio.OpusEncoder
	buf     []byte

	mu        sync.Mutex
	seqNum    uint16
	timestamp uint32
	ssrc      uint32
}

func newWebRTCMedia(remote *webrtc.TrackRemote, local *webrtc.TrackLocalStaticRTP) (*webrtcMedia, error) {
	dec, err := audio.NewOpusDecoder()
	if err != nil {
		return nil, err
	}
	enc, err := audio.NewOpusEncoder()
	if err != nil {
		return nil, err
	}
	return &webrtcMedia{remote: remote, local: local, decoder: dec, encoder: enc, buf: make([]byte, 1500), ssrc: 12345678}, nil
}

func (m *webrtcMedia) ReadFrame() ([]int16, error) {
	for {
		n, _, err := m.remote.Read(m.buf)
		if err != nil {
			return nil, err
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(m.buf[:n]); err != nil {
			continue
		}
		pcm, err := m.decoder.Decode(pkt.Payload)
		if err != nil {
			continue
		}
		return pcm, nil
	}
}

func (m *webrtcMedia) WriteFrame(samples []int16, talkspurt bool) error {
	opusData, err := m.encoder.Encode(samples)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.seqNum++
	m.timestamp += audio.RTPTimestampIncr
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: m.seqNum,
			Timestamp:      m.timestamp,
			SSRC:           m.ssrc,
			Marker:         talkspurt,
		},
		Payload: opusData,
	}
	m.mu.Unlock()
	raw, err := pkt.Marshal()
	if err != nil {
		return err
	}
	_, err = m.local.Write(raw)
	return err
}
