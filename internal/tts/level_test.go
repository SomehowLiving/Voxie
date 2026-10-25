package tts

import (
	"encoding/binary"
	"math"
	"testing"
)

func tone(amp float64, seconds float64) []byte {
	n := int(seconds * 16000)
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(amp * 32767 * math.Sin(2*math.Pi*220*float64(i)/16000))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func stats(pcm []byte) (rmsDB, peak float64) {
	n := len(pcm) / 2
	var sum float64
	for i := 0; i < n; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		sum += s * s
		peak = math.Max(peak, math.Abs(s))
	}
	return 10 * math.Log10(sum/float64(n)), peak
}

func TestLevelPCMBringsLoudAndQuietVoicesTogether(t *testing.T) {
	loud, _ := stats(levelPCM(tone(0.22, 1)))  // Sarvam-like: about -16 dBFS
	quiet, _ := stats(levelPCM(tone(0.06, 1))) // a quiet voice
	if math.Abs(loud-quiet) > 3 {
		t.Errorf("after leveling: loud %.1f dB, quiet %.1f dB; want within 3 dB", loud, quiet)
	}
	if _, peak := stats(levelPCM(tone(0.99, 1))); peak > levelPeak+0.01 {
		t.Errorf("peak %.2f, want at most %.2f", peak, levelPeak)
	}
}

func TestLevelPCMLeavesSilenceAlone(t *testing.T) {
	silent := make([]byte, 3200)
	if out := levelPCM(silent); string(out) != string(silent) {
		t.Error("silence changed")
	}
}
