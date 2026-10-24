package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

type recorder struct{ events []string }

type fakeComponent struct {
	name     string
	rec      *recorder
	startErr error
}

func (f *fakeComponent) Name() string { return f.name }

func (f *fakeComponent) Start(context.Context) error {
	f.rec.events = append(f.rec.events, "start "+f.name)
	return f.startErr
}

func (f *fakeComponent) Stop(context.Context) error {
	f.rec.events = append(f.rec.events, "stop "+f.name)
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRunStartsInOrderAndStopsInReverse(t *testing.T) {
	rec := &recorder{}
	a := New(discardLogger(), &fakeComponent{name: "a", rec: rec}, &fakeComponent{name: "b", rec: rec})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"start a", "start b", "stop b", "stop a"}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
}

func TestRunStopsStartedComponentsOnStartFailure(t *testing.T) {
	rec := &recorder{}
	boom := errors.New("boom")
	a := New(discardLogger(),
		&fakeComponent{name: "a", rec: rec},
		&fakeComponent{name: "b", rec: rec, startErr: boom},
		&fakeComponent{name: "c", rec: rec},
	)
	err := a.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want %v", err, boom)
	}
	want := []string{"start a", "start b", "stop a"}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
}
