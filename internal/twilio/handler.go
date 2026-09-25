package twilio

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/streamcoreai/streamcore-server/internal/session"
)

// NewHandler serves Twilio Media Streams at a WebSocket URL (Voxie mounts it
// at /twilio/media). A call reaches it through TwiML like:
//
//	<Response><Connect><Stream url="wss://<voxie-host>/twilio/media">
//	  <Parameter name="resource_id" value="+919812345678"/>
//	</Stream></Connect></Response>
//
// Each stream is one call: a session keyed on Twilio's CallSid, running the
// same pipeline a WebRTC call does. `resource_id` becomes the agent's
// resource_id (the dialled number, so the agent knows who it's calling), and
// `direction` defaults to "outbound".
//
// With authToken set (TWILIO_AUTH_TOKEN), a handshake without a valid
// X-Twilio-Signature is refused, so only Twilio can open calls here.
// publicURL, when set, is the wss:// URL as Twilio sees it -- needed behind a
// proxy that rewrites the Host header; otherwise it's rebuilt from the request.
func NewHandler(sm *session.Manager, authToken, publicURL string) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		// Twilio isn't a browser; the signature, not the Origin, authenticates it.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if authToken != "" && !validSignature(r, authToken, publicURL) {
			log.Printf("[twilio] refused a stream with a missing or invalid X-Twilio-Signature")
			http.Error(w, "invalid Twilio signature", http.StatusForbidden)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade already replied
		}
		stream, info, err := Accept(conn, 10*time.Second)
		if err != nil {
			log.Printf("[twilio] stream never started: %v", err)
			_ = conn.Close()
			return
		}
		defer stream.Close()

		id := "twilio-" + info.CallSid
		sess := sm.GetOrCreate(id)
		if sess == nil {
			log.Printf("[twilio] call %s refused: at session capacity", info.CallSid)
			return
		}
		defer sm.Remove(id)
		sess.SetResourceID(info.Parameters["resource_id"])

		direction := info.Parameters["direction"]
		if direction == "" {
			direction = "outbound"
		}
		// The request's context ends at the hijack, so the call gets its own,
		// ended when Twilio stops the stream (the caller hung up).
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-stream.Done()
			cancel()
		}()

		log.Printf("[twilio] call %s started (stream %s, %s)", info.CallSid, info.StreamSid, direction)
		start := time.Now()
		// A phone line has no echo cancellation Voxie can rely on.
		err = sess.RunMedia(ctx, stream, session.PeerOptions{Direction: direction, AECAbsent: true})
		if err != nil {
			log.Printf("[twilio] call %s failed: %v", info.CallSid, err)
		}
		log.Printf("[twilio] call %s ended after %s", info.CallSid, time.Since(start).Round(time.Second))
	}
}

// validSignature checks X-Twilio-Signature: base64(HMAC-SHA1(auth token,
// URL)). A WebSocket handshake has no form body, so the URL is all that's
// signed.
func validSignature(r *http.Request, authToken, publicURL string) bool {
	given := r.Header.Get("X-Twilio-Signature")
	if given == "" {
		return false
	}
	url := publicURL
	if url == "" {
		host := r.Host
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			host = fwd
		}
		url = "wss://" + host + r.URL.RequestURI()
	}
	return subtle.ConstantTimeCompare([]byte(sign(authToken, url)), []byte(given)) == 1
}

func sign(authToken, url string) string {
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(url))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
