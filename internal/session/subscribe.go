package session

import "sync"

// Subscribe returns a channel of snapshots, starting with the current one,
// and a function to stop the subscription. Delivery is ordered and never
// blocks the Manager: a slow reader gets a backlog, not lost updates, so
// every snapshot of a cancelled generation reaches the reader before the
// terminal snapshot and nothing of it comes after (US-10). The channel is
// closed on unsubscribe or Close.
func (m *Manager) Subscribe() (<-chan Snapshot, func()) {
	var s *subscriber
	ok := m.exec(func() {
		s = newSubscriber()
		m.subs[s] = struct{}{}
		s.push(*m.current.Load())
		m.subsWG.Add(1)
		go s.run(&m.subsWG)
	})
	if !ok {
		ch := make(chan Snapshot)
		close(ch)
		return ch, func() {}
	}
	return s.out, func() {
		if !m.exec(func() { delete(m.subs, s); s.stop() }) {
			s.stop()
		}
	}
}

type subscriber struct {
	out      chan Snapshot
	wake     chan struct{}
	quit     chan struct{}
	stopOnce sync.Once

	mu    sync.Mutex
	queue []Snapshot
}

func newSubscriber() *subscriber {
	return &subscriber{
		out:  make(chan Snapshot),
		wake: make(chan struct{}, 1),
		quit: make(chan struct{}),
	}
}

func (s *subscriber) push(snap Snapshot) {
	s.mu.Lock()
	s.queue = append(s.queue, snap)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) stop() { s.stopOnce.Do(func() { close(s.quit) }) }

func (s *subscriber) run(wg *sync.WaitGroup) {
	defer wg.Done()
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			select {
			case <-s.wake:
				continue
			case <-s.quit:
				return
			}
		}
		next := s.queue[0]
		s.queue[0] = Snapshot{}
		s.queue = s.queue[1:]
		s.mu.Unlock()
		select {
		case s.out <- next:
		case <-s.quit:
			return
		}
	}
}
