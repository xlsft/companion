// Package app owns process lifecycle and dependency wiring for the core.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Component is a long-lived part of the core started and stopped by App.
type Component interface {
	Name() string
	// Start must return once the component is ready; background work runs
	// in its own goroutines bound to ctx.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// App starts components in order and stops them in reverse order.
type App struct {
	log         *slog.Logger
	components  []Component
	stopTimeout time.Duration
}

func New(log *slog.Logger, components ...Component) *App {
	return &App{log: log, components: components, stopTimeout: 10 * time.Second}
}

// Run starts every component, blocks until ctx is cancelled and then stops
// them. If a component fails to start, the already started ones are stopped.
func (a *App) Run(ctx context.Context) error {
	started := make([]Component, 0, len(a.components))
	for _, c := range a.components {
		a.log.Info("starting component", "component", c.Name())
		if err := c.Start(ctx); err != nil {
			startErr := fmt.Errorf("start %s: %w", c.Name(), err)
			return errors.Join(startErr, a.stop(started))
		}
		started = append(started, c)
	}
	<-ctx.Done()
	return a.stop(started)
}

func (a *App) stop(started []Component) error {
	ctx, cancel := context.WithTimeout(context.Background(), a.stopTimeout)
	defer cancel()
	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		c := started[i]
		a.log.Info("stopping component", "component", c.Name())
		if err := c.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", c.Name(), err))
		}
	}
	return errors.Join(errs...)
}
