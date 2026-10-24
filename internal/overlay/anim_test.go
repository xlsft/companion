package overlay

import "testing"

// On open paths the glint must fade out past the end and back in at the
// start: its opacity is 0 where the phase wraps and full mid-path.
func TestOpenGlintFadesAtTheEnds(t *testing.T) {
	tests := []struct {
		phase        float64
		envLo, envHi float64
		posLo, posHi float64
	}{
		{0, 0, 0, -0.2, 0},
		{0.999, 0, 0.01, 1, 1.2},
		{0.5, 1, 1, 0.45, 0.55},
		{0.08, 0.1, 0.9, -0.1, 0.1},
	}
	for _, tt := range tests {
		pos, env := openGlint(tt.phase)
		if env < tt.envLo || env > tt.envHi || pos < tt.posLo || pos > tt.posHi {
			t.Errorf("phase %v: pos %v env %v", tt.phase, pos, env)
		}
	}
}
