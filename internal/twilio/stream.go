package twilio

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Twilio Media Streams messages (https://www.twilio.com/docs/voice/media-streams/websocket-messages).
type inbound struct {
	Event     string `json:"event"`
	StreamSid string `json:"streamSid"`
	Start     *struct {
		StreamSid        string            `json:"streamSid"`
		CallSid          string            `json:"callSid"`
		AccountSid       string            `json:"accountSid"`
		CustomParameters map[string]string `json:"customParameters"`
		MediaFormat      struct {
			Encoding   string `json:"encoding"`
			SampleRate int    `json:"sampleRate"`
		} `json:"mediaFormat"`
	} `json:"start"`
	Media *struct {
		Track   string `json:"track"`
		Payload string `json:"payload"`
	} `json:"media"`
}

type outboundMedia struct {
	Event     string `json:"event"`
	StreamSid string `json:"streamSid"`
	Media     struct {
		Payload string `json:"payload"`
	} `json:"media"`
}

type outboundClear struct {
	Event     string `json:"event"`
	StreamSid string `json:"streamSid"`
}

// StartInfo is what Twilio says about the call when the stream opens.
type StartInfo struct {
	StreamSid  string
	CallSid    string
	Parameters map[string]string // the TwiML <Parameter>s
}

// Stream is one Twilio Media Stream as the pipeline's Media: μ-law 8kHz
// from Twilio becomes 16kHz PCM frames, and the agent's frames go back as
// μ-law. It implements pipeline.Media and pipeline.MediaClearer.
type Stream struct {
	conn      *websocket.Conn
	streamSid string
	frames    chan []int16
	done      chan struct{}
	closeOnce sync.Once

	up   upsampler
	down downsampler

	writeMu sync.Mutex
}

// Accept reads the stream's opening messages until "start", which names the
// call and carries its TwiML parameters.
func Accept(conn *websocket.Conn, timeout time.Duration) (*Stream, StartInfo, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var msg inbound
		if err := conn.ReadJSON(&msg); err != nil {
			return nil, StartInfo{}, err
		}
		if msg.Event != "start" || msg.Start == nil {
			continue // "connected" comes first
		}
		if enc := msg.Start.MediaFormat.Encoding; enc != "" && enc != "audio/x-mulaw" {
			return nil, StartInfo{}, errors.New("unsupported media encoding " + enc)
		}
		_ = conn.SetReadDeadline(time.Time{})
		s := &Stream{
			conn:      conn,
			streamSid: msg.Start.StreamSid,
			frames:    make(chan []int16, 50),
			done:      make(chan struct{}),
		}
		go s.readLoop()
		return s, StartInfo{StreamSid: msg.Start.StreamSid, CallSid: msg.Start.CallSid, Parameters: msg.Start.CustomParameters}, nil
	}
}

func (s *Stream) readLoop() {
	defer s.Close()
	// Twilio sends 20ms per message, but a message can carry more or less;
	// re-frame into exact 20ms frames for the pipeline.
	var pending []int16
	for {
		var msg inbound
		if err := s.conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Event {
		case "media":
			if msg.Media == nil || (msg.Media.Track != "" && msg.Media.Track != "inbound") {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(msg.Media.Payload)
			if err != nil {
				continue
			}
			pcm8 := make([]int16, len(raw))
			for i, b := range raw {
				pcm8[i] = mulawDecode(b)
			}
			pending = append(pending, s.up.process(pcm8)...)
			for len(pending) >= 2*phoneFrameBytes {
				frame := make([]int16, 2*phoneFrameBytes)
				copy(frame, pending)
				pending = pending[2*phoneFrameBytes:]
				select {
				case s.frames <- frame:
				case <-s.done:
					return
				default:
					// The pipeline fell behind by a second: drop rather than
					// block the socket, which would stall Twilio's stream.
				}
			}
		case "stop":
			return
		}
	}
}

// ReadFrame returns the caller's next 20ms at 16kHz; io.EOF once the call
// has ended.
func (s *Stream) ReadFrame() ([]int16, error) {
	select {
	case f := <-s.frames:
		return f, nil
	case <-s.done:
		return nil, io.EOF
	}
}

// WriteFrame sends 20ms of the agent's voice.
func (s *Stream) WriteFrame(samples []int16, _ bool) error {
	pcm8 := s.down.process(samples)
	raw := make([]byte, len(pcm8))
	for i, v := range pcm8 {
		raw[i] = mulawEncode(v)
	}
	msg := outboundMedia{Event: "media", StreamSid: s.streamSid}
	msg.Media.Payload = base64.StdEncoding.EncodeToString(raw)
	return s.write(msg)
}

// Clear drops the agent audio Twilio has buffered but not yet played, so
// barge-in stops the agent at once.
func (s *Stream) Clear() {
	if err := s.write(outboundClear{Event: "clear", StreamSid: s.streamSid}); err != nil {
		log.Printf("[twilio] clear: %v", err)
	}
}

func (s *Stream) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return s.conn.WriteMessage(websocket.TextMessage, b)
}

// Done closes when the call's stream ends: Twilio sent "stop", or the
// socket closed.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Close ends the stream. Closing the socket ends the <Connect><Stream> verb,
// and with nothing after it in the TwiML, Twilio hangs up the call.
func (s *Stream) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.writeMu.Lock()
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		s.writeMu.Unlock()
		_ = s.conn.Close()
	})
}
