package spectrum

import (
	"math"
	"testing"
)

func sine(hz float64, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * 32767 * math.Sin(2*math.Pi*hz*float64(i)/SampleRate))
	}
	return out
}

func settle(a *Analyzer, samples []int16) []byte {
	var frame []byte
	for i := 0; i < 30; i++ {
		a.Push(samples)
		frame = a.Frame(frame)
	}
	return frame
}

func loudestBand(frame []byte) int {
	best := 0
	for i, v := range frame {
		if v > frame[best] {
			best = i
		}
	}
	return best
}

func TestToneLandsInRisingBands(t *testing.T) {
	low := loudestBand(settle(New(32), sine(110, 512, 0.5)))
	high := loudestBand(settle(New(32), sine(2000, 512, 0.5)))
	if low >= high {
		t.Fatalf("110 Hz peaked at band %d, 2 kHz at band %d; want low < high", low, high)
	}
	if low > 8 || high < 20 {
		t.Fatalf("bands out of place: 110 Hz at %d, 2 kHz at %d", low, high)
	}
}

func TestSilenceIsFlat(t *testing.T) {
	frame := settle(New(32), make([]int16, 512))
	for i, v := range frame {
		if v != 0 {
			t.Fatalf("band %d reads %d in silence", i, v)
		}
	}
}

func TestEdgesStrictlyRise(t *testing.T) {
	a := New(48)
	for i := 1; i < len(a.edges); i++ {
		if a.edges[i] <= a.edges[i-1] {
			t.Fatalf("edge %d (%d) not above edge %d (%d)", i, a.edges[i], i-1, a.edges[i-1])
		}
	}
}
