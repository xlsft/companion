package stub

import (
	"context"
	"errors"
	"testing"
)

func TestSignalsInactive(t *testing.T) {
	for _, s := range Signals() {
		a, err := s.Active(context.Background())
		if a || !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s: %v %v", s.Name(), a, err)
		}
	}
}
