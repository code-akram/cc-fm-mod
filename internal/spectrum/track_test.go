package spectrum

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

// TestTrack prints how each column behaves over a whole track, for tuning.
// It runs only when CC_FM_TRACK names raw mono s16le PCM at SampleRate:
//
//	ffmpeg -i song.ogg -ac 1 -ar 24000 -f s16le song.raw
//	CC_FM_TRACK=song.raw go test -run TestTrack -v ./internal/spectrum/
func TestTrack(t *testing.T) {
	path := os.Getenv("CC_FM_TRACK")
	if path == "" {
		t.Skip("CC_FM_TRACK not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}

	const columns, perFrame = 24, SampleRate / 30
	a := New(32)
	var frames [][]float64
	var frame []byte
	for i := 0; i+perFrame <= len(samples); i += perFrame {
		a.Push(samples[i : i+perFrame])
		frame = a.Frame(frame)
		row := make([]float64, columns)
		for c := range columns {
			from := c * len(frame) / columns
			to := max(from+1, (c+1)*len(frame)/columns)
			for _, v := range frame[from:to] {
				row[c] = math.Max(row[c], float64(v))
			}
		}
		frames = append(frames, row)
	}

	glyphs := []rune("▁▂▃▄▅▆▇█")
	for f := 0; f < len(frames); f += len(frames) / 16 {
		var b strings.Builder
		for _, v := range frames[f] {
			b.WriteRune(glyphs[min(7, int(v)*8/256)])
		}
		t.Logf("%5.1fs %s", float64(f)/30, b.String())
	}
	var mean, move strings.Builder
	for c := range columns {
		var sum, sq float64
		for _, row := range frames {
			sum += row[c]
		}
		m := sum / float64(len(frames))
		for _, row := range frames {
			sq += (row[c] - m) * (row[c] - m)
		}
		fmt.Fprintf(&mean, "%4d", int(m))
		fmt.Fprintf(&move, "%4d", int(math.Sqrt(sq/float64(len(frames)))))
	}
	t.Logf("mean %s", mean.String())
	t.Logf("move %s", move.String())
}
