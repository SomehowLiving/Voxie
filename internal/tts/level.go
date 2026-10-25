package tts

import (
	"encoding/binary"
	"math"
)

// Loudness leveling for voices that return a whole sentence at once (the
// voice router). Its voices differ a lot: measured, Sarvam's Hindi and Tamil
// came out 8.5 dB louder than Kokoro's English (-17.7 vs -26.1 dBFS) and
// peaked at -0.1 dBFS, so a call that moved from English to Hindi suddenly
// got almost three times as loud, at the edge of distortion.
const (
	levelTargetDB = -24.0 // speech loudness every sentence is brought to
	levelMaxGain  = 6.0   // dB: a quiet sentence is lifted at most this much
	levelMinGain  = -12.0 // dB: a loud one is lowered at most this much
	levelPeak     = 0.7   // of full scale (-3 dBFS): no peak above it
	levelGate     = -50.0 // dBFS: 20 ms frames below this are silence, not speech
)

// levelPCM brings a sentence of linear16 PCM (16 kHz mono) to
// levelTargetDB, measured over its speech, and keeps its peaks under
// levelPeak.
func levelPCM(pcm []byte) []byte {
	n := len(pcm) / 2
	if n == 0 {
		return pcm
	}
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
	}
	const frame = 320 // 20 ms
	var speech float64
	var count int
	peak := 0.0
	for start := 0; start < n; start += frame {
		end := min(start+frame, n)
		var sum float64
		for _, s := range samples[start:end] {
			sum += s * s
			peak = math.Max(peak, math.Abs(s))
		}
		if 10*math.Log10(sum/float64(end-start)+1e-12) > levelGate {
			speech += sum
			count += end - start
		}
	}
	if count == 0 {
		return pcm
	}
	rmsDB := 10 * math.Log10(speech/float64(count)+1e-12)
	gainDB := math.Max(levelMinGain, math.Min(levelMaxGain, levelTargetDB-rmsDB))
	gain := math.Pow(10, gainDB/20)
	if peak*gain > levelPeak {
		gain = levelPeak / peak
	}
	out := make([]byte, len(pcm))
	for i, s := range samples {
		v := math.Round(s * gain * 32768)
		v = math.Max(-32768, math.Min(32767, v))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
	}
	return out
}
