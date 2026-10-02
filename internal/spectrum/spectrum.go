// Package spectrum turns mono PCM into a row of bar heights, one byte per band.
package spectrum

import "math"

const (
	// SampleRate is the rate the analyzer expects its samples at: the
	// player's 48 kHz, halved.
	SampleRate = 24000

	fftSize = 2048
	minHz   = 45.0
	maxHz   = 10000.0

	// rangeDB is how far below the running peak a band reads as empty.
	rangeDB = 30.0
	// headroomDB is how far above the average band full height sits.
	headroomDB = 14.0
	// peakFloor keeps quiet passages and silence from being blown up to full scale.
	peakFloor = -38.0
	// peakFall is how fast the auto-gain eases back per frame (about 4.5 dB/s at 30 fps).
	peakFall = 0.15
	// tiltDB lifts the highs per octave, so a typical mix doesn't read as all bass.
	tiltDB = 3.0
	// spread is how fast a band's energy fades into its neighbours (the
	// "monstercat" filter): each step away divides it by this much.
	spread = 1.6

	// The adaptive EQ: each band keeps a slow running average of its level,
	// and bands that sit quieter than the rest are lifted toward them. Lo-fi
	// rolls off its highs on purpose; without this the right of the row
	// would barely move.
	eqRate     = 1.0 / (30 * 8) // about 8 s to settle at 30 fps
	eqStrength = 0.8
	eqMaxBoost = 12.0
	eqMaxCut   = 6.0
)

// Analyzer keeps the last fftSize samples and computes bands on demand.
// It is not safe for concurrent use.
type Analyzer struct {
	ring   []float64
	pos    int
	window []float64
	edges  []int
	tilt   []float64
	db     []float64
	avg    []float64
	target []float64
	level  []float64
	peak   float64
	re, im []float64

	isEqWarm bool
}

// New returns an analyzer producing the given number of log-spaced bands.
func New(bands int) *Analyzer {
	a := &Analyzer{
		ring:   make([]float64, fftSize),
		window: make([]float64, fftSize),
		edges:  make([]int, bands+1),
		tilt:   make([]float64, bands),
		db:     make([]float64, bands),
		avg:    make([]float64, bands),
		target: make([]float64, bands),
		level:  make([]float64, bands),
		peak:   peakFloor,
		re:     make([]float64, fftSize),
		im:     make([]float64, fftSize),
	}
	for i := range a.window {
		a.window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
	}

	binHz := float64(SampleRate) / fftSize
	for i := 0; i <= bands; i++ {
		hz := minHz * math.Pow(maxHz/minHz, float64(i)/float64(bands))
		bin := int(math.Round(hz / binHz))
		if i > 0 && bin <= a.edges[i-1] {
			bin = a.edges[i-1] + 1
		}
		a.edges[i] = min(bin, fftSize/2)
	}
	for b := range a.tilt {
		center := math.Sqrt(float64(a.edges[b]*max(a.edges[b+1]-1, a.edges[b]))) * binHz
		a.tilt[b] = tiltDB * math.Log2(math.Max(center, minHz)/minHz)
	}

	return a
}

// Bands is the number of bars each frame holds.
func (a *Analyzer) Bands() int { return len(a.level) }

// Push appends samples to the analysis window.
func (a *Analyzer) Push(samples []int16) {
	for _, s := range samples {
		a.ring[a.pos] = float64(s) / 32768
		a.pos = (a.pos + 1) % fftSize
	}
}

// Reset drops the window and the smoothing, as after a stop.
func (a *Analyzer) Reset() {
	clear(a.ring)
	clear(a.level)
	clear(a.avg)
	a.isEqWarm = false
	a.peak = peakFloor
}

// Frame computes one frame of bar heights (0–255) into out and returns it.
func (a *Analyzer) Frame(out []byte) []byte {
	for i := range fftSize {
		a.re[i] = a.ring[(a.pos+i)%fftSize] * a.window[i]
		a.im[i] = 0
	}
	fft(a.re, a.im)

	raw := math.Inf(-1)
	for b := range a.db {
		var mag float64
		for k := a.edges[b]; k < a.edges[b+1]; k++ {
			mag = math.Max(mag, math.Hypot(a.re[k], a.im[k]))
		}
		// A full-scale sine peaks at fftSize/4 under a Hann window: that's 0 dB.
		a.db[b] = 20*math.Log10(mag/(fftSize/4)+1e-9) + a.tilt[b]
		raw = math.Max(raw, a.db[b])
	}
	a.equalize(raw > peakFloor-rangeDB)

	// Auto-gain follows the average band plus headroom, not the loudest band:
	// a kick then tops out its own bars instead of pushing every other bar
	// down for the seconds the gain takes to recover.
	var mean float64
	for _, db := range a.db {
		mean += db
	}
	level := mean/float64(len(a.db)) + headroomDB

	// Jump up to a loud frame at once, ease back down over seconds.
	if level > a.peak {
		a.peak = level
	} else {
		a.peak = math.Max(a.peak-peakFall, peakFloor)
	}

	for b, db := range a.db {
		a.target[b] = math.Min(math.Max((db-(a.peak-rangeDB))/rangeDB, 0), 1)
	}
	// Let each band lift its neighbours, so the row reads as one shape rather
	// than lone spikes over gaps.
	for b, v := range a.target {
		fall := v
		for j := b + 1; j < len(a.target); j++ {
			if fall /= spread; fall <= a.target[j] {
				break
			}
			a.target[j] = fall
		}
		fall = v
		for j := b - 1; j >= 0; j-- {
			if fall /= spread; fall <= a.target[j] {
				break
			}
			a.target[j] = fall
		}
	}

	out = out[:0]
	for b, v := range a.target {
		// Fast attack, slower release: bars snap up on a hit and settle back.
		if v > a.level[b] {
			a.level[b] += (v - a.level[b]) * 0.7
		} else {
			a.level[b] += (v - a.level[b]) * 0.25
		}
		out = append(out, byte(math.Round(a.level[b]*255)))
	}

	return out
}

// equalize lifts bands that run quieter than the rest, learning each band's
// level only while there is sound, so silence doesn't skew it.
func (a *Analyzer) equalize(isSounding bool) {
	if isSounding {
		if !a.isEqWarm {
			copy(a.avg, a.db)
			a.isEqWarm = true
		}
		for b, db := range a.db {
			a.avg[b] += (db - a.avg[b]) * eqRate
		}
	}
	if !a.isEqWarm {
		return
	}
	var mean float64
	for _, v := range a.avg {
		mean += v
	}
	mean /= float64(len(a.avg))
	for b := range a.db {
		a.db[b] += math.Min(math.Max((mean-a.avg[b])*eqStrength, -eqMaxCut), eqMaxBoost)
	}
}

// fft is an in-place iterative radix-2 transform; len(re) must be a power of two.
func fft(re, im []float64) {
	n := len(re)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		angle := -2 * math.Pi / float64(size)
		wr, wi := math.Cos(angle), math.Sin(angle)
		for start := 0; start < n; start += size {
			cr, ci := 1.0, 0.0
			for k := range size / 2 {
				a, b := start+k, start+k+size/2
				tr := re[b]*cr - im[b]*ci
				ti := re[b]*ci + im[b]*cr
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a] += tr
				im[a] += ti
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
}
