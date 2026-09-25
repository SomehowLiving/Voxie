package twilio

import "math"

// Phone audio: Twilio Media Streams carry G.711 μ-law at 8kHz, 20ms (160
// bytes) per message. The pipeline runs on 16kHz PCM, so audio is decoded
// and upsampled on the way in, and low-pass filtered, decimated and
// encoded on the way out.

const (
	phoneRate       = 8000
	phoneFrameBytes = 160 // 20ms at 8kHz, one byte per μ-law sample
)

// mulawDecode expands one G.711 μ-law byte to 16-bit linear PCM.
func mulawDecode(b byte) int16 {
	b = ^b
	sign := b & 0x80
	exponent := (b >> 4) & 0x07
	mantissa := b & 0x0F
	sample := ((int32(mantissa) << 3) + 0x84) << exponent
	sample -= 0x84
	if sign != 0 {
		return int16(-sample)
	}
	return int16(sample)
}

// mulawEncode compresses one 16-bit linear PCM sample to G.711 μ-law.
func mulawEncode(s int16) byte {
	const bias = 0x84
	const clip = 32635
	sample := int32(s)
	sign := byte(0)
	if sample < 0 {
		sample = -sample
		sign = 0x80
	}
	if sample > clip {
		sample = clip
	}
	sample += bias
	exponent := byte(7)
	for mask := int32(0x4000); sample&mask == 0 && exponent > 0; mask >>= 1 {
		exponent--
	}
	mantissa := byte((sample >> (exponent + 3)) & 0x0F)
	return ^(sign | (exponent << 4) | mantissa)
}

// upsampler doubles 8kHz to 16kHz by linear interpolation, carrying the
// last sample across frames so frame edges don't click. Phone audio has
// nothing above 4kHz, so interpolation loses nothing speech-to-text uses.
type upsampler struct {
	last int16
}

func (u *upsampler) process(in []int16) []int16 {
	out := make([]int16, 2*len(in))
	prev := int32(u.last)
	for i, s := range in {
		out[2*i] = int16((prev + int32(s)) / 2)
		out[2*i+1] = s
		prev = int32(s)
	}
	if len(in) > 0 {
		u.last = in[len(in)-1]
	}
	return out
}

// downsampler halves 16kHz to 8kHz. The agent's voice has energy up to
// 8kHz, which would fold back as hiss below 4kHz if samples were simply
// dropped, so a windowed-sinc low-pass (cutoff ~3.4kHz) runs first. Its
// history carries across frames.
type downsampler struct {
	history []float64
}

// lowpass is a 31-tap Hamming-windowed sinc, cutoff 3400Hz at 16kHz,
// normalised to unity gain at DC.
var lowpass = func() []float64 {
	const taps = 31
	const cutoff = 3400.0 / 16000.0
	h := make([]float64, taps)
	sum := 0.0
	for i := range h {
		n := float64(i - (taps-1)/2)
		var sinc float64
		if n == 0 {
			sinc = 2 * cutoff
		} else {
			sinc = math.Sin(2*math.Pi*cutoff*n) / (math.Pi * n)
		}
		window := 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(taps-1))
		h[i] = sinc * window
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	return h
}()

func (d *downsampler) process(in []int16) []int16 {
	taps := len(lowpass)
	if d.history == nil {
		d.history = make([]float64, taps-1)
	}
	buf := make([]float64, len(d.history)+len(in))
	copy(buf, d.history)
	for i, s := range in {
		buf[len(d.history)+i] = float64(s)
	}
	out := make([]int16, len(in)/2)
	for i := range out {
		// Output sample i is centred on input sample 2i.
		end := len(d.history) + 2*i
		acc := 0.0
		for k := 0; k < taps; k++ {
			acc += lowpass[k] * buf[end-k]
		}
		out[i] = clamp16(acc)
	}
	copy(d.history, buf[len(buf)-len(d.history):])
	return out
}

func clamp16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
