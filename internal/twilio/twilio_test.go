package twilio

import (
	"encoding/base64"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMulawRoundTrip(t *testing.T) {
	for _, s := range []int16{0, 1, -1, 100, -100, 1000, -1000, 8000, -8000, 32000, -32000} {
		got := mulawDecode(mulawEncode(s))
		// μ-law keeps ~13 bits: the error grows with the level, ~3% at most.
		tol := math.Max(8, math.Abs(float64(s))*0.04)
		if math.Abs(float64(got-s)) > tol {
			t.Errorf("round trip %d -> %d (tolerance %.0f)", s, got, tol)
		}
	}
	// G.711 reference points: 0xFF is silence, 0x00 the negative peak.
	if mulawDecode(0xFF) != 0 {
		t.Errorf("0xFF should decode to 0, got %d", mulawDecode(0xFF))
	}
	if mulawDecode(0x00) != -32124 {
		t.Errorf("0x00 should decode to -32124, got %d", mulawDecode(0x00))
	}
}

func tone(freq float64, n int, rate float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(10000 * math.Sin(2*math.Pi*freq*float64(i)/rate))
	}
	return out
}

func rms(s []int16) float64 {
	sum := 0.0
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}

// Downsampling must keep speech (1kHz) and remove what would alias (6kHz
// folds to 2kHz if merely decimated).
func TestDownsamplerKeepsSpeechAndRemovesAliasing(t *testing.T) {
	for _, c := range []struct {
		freq     float64
		min, max float64
	}{{1000, 0.9, 1.1}, {6000, 0, 0.05}} {
		var d downsampler
		var out []int16
		in := tone(c.freq, 16000, 16000)
		for i := 0; i+320 <= len(in); i += 320 { // frame by frame, as the pipeline sends
			out = append(out, d.process(in[i:i+320])...)
		}
		ratio := rms(out[400:]) / rms(in)
		if ratio < c.min || ratio > c.max {
			t.Errorf("%.0fHz: output/input level %.3f, want %.2f..%.2f", c.freq, ratio, c.min, c.max)
		}
	}
}

func TestUpsamplerDoublesAndIsContinuousAcrossFrames(t *testing.T) {
	var u upsampler
	a := u.process([]int16{0, 100})
	b := u.process([]int16{200})
	if len(a) != 4 || len(b) != 2 {
		t.Fatalf("lengths %d, %d", len(a), len(b))
	}
	if b[0] != 150 { // interpolated between the previous frame's last sample and this one
		t.Errorf("first sample of the next frame = %d, want 150", b[0])
	}
}

func TestSignature(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/twilio/media", nil)
	r.Host = "voxie.example.ngrok-free.dev"
	r.Header.Set("X-Twilio-Signature", sign("token", "wss://voxie.example.ngrok-free.dev/twilio/media"))
	if !validSignature(r, "token", "") {
		t.Error("valid signature rejected")
	}
	if validSignature(r, "other-token", "") {
		t.Error("signature under another token accepted")
	}
	if !validSignature(r, "token", "wss://voxie.example.ngrok-free.dev/twilio/media") {
		t.Error("valid signature rejected with an explicit public URL")
	}
	r.Header.Del("X-Twilio-Signature")
	if validSignature(r, "token", "") {
		t.Error("missing signature accepted")
	}
}

// A fake Twilio on the other end of a real WebSocket.
func TestStreamSpeaksTwilioMediaStreams(t *testing.T) {
	accepted := make(chan *Stream, 1)
	infoCh := make(chan StartInfo, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		s, info, err := Accept(conn, 2*time.Second)
		if err != nil {
			t.Error(err)
			return
		}
		infoCh <- info
		accepted <- s
	}))
	defer srv.Close()

	twilio, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer twilio.Close()
	send := func(v any) {
		if err := twilio.WriteJSON(v); err != nil {
			t.Fatal(err)
		}
	}
	send(map[string]any{"event": "connected", "protocol": "Call", "version": "1.0.0"})
	send(map[string]any{"event": "start", "streamSid": "MZ1", "start": map[string]any{
		"streamSid": "MZ1", "callSid": "CA1",
		"customParameters": map[string]string{"resource_id": "+919800000000"},
		"mediaFormat":      map[string]any{"encoding": "audio/x-mulaw", "sampleRate": 8000, "channels": 1},
	}})
	stream := <-accepted
	info := <-infoCh
	if info.CallSid != "CA1" || info.Parameters["resource_id"] != "+919800000000" {
		t.Fatalf("start info %+v", info)
	}

	// 30ms of caller audio in two uneven messages -> one 20ms frame now.
	payload := make([]byte, 240)
	for i := range payload {
		payload[i] = mulawEncode(4000)
	}
	send(map[string]any{"event": "media", "streamSid": "MZ1", "media": map[string]any{"track": "inbound", "payload": base64.StdEncoding.EncodeToString(payload[:100])}})
	send(map[string]any{"event": "media", "streamSid": "MZ1", "media": map[string]any{"track": "inbound", "payload": base64.StdEncoding.EncodeToString(payload[100:])}})
	frame, err := stream.ReadFrame()
	if err != nil || len(frame) != 320 {
		t.Fatalf("frame len %d, err %v", len(frame), err)
	}
	if frame[100] < 3500 || frame[100] > 4500 {
		t.Errorf("decoded level %d, want ~4000", frame[100])
	}

	// The agent's voice goes back as one μ-law media message per frame.
	if err := stream.WriteFrame(tone(500, 320, 16000), true); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Event     string `json:"event"`
		StreamSid string `json:"streamSid"`
		Media     struct {
			Payload string `json:"payload"`
		} `json:"media"`
	}
	_ = twilio.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := twilio.ReadJSON(&out); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(out.Media.Payload)
	if out.Event != "media" || out.StreamSid != "MZ1" || len(raw) != 160 {
		t.Fatalf("outbound %s %s, %d bytes", out.Event, out.StreamSid, len(raw))
	}

	// Barge-in clears Twilio's buffer.
	stream.Clear()
	var clear map[string]string
	if err := twilio.ReadJSON(&clear); err != nil || clear["event"] != "clear" || clear["streamSid"] != "MZ1" {
		t.Fatalf("clear message %v, err %v", clear, err)
	}

	// Twilio's stop ends the call.
	send(map[string]any{"event": "stop", "streamSid": "MZ1"})
	select {
	case <-stream.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stop didn't end the stream")
	}
	for {
		if _, err := stream.ReadFrame(); err == io.EOF {
			break
		}
	}
}
