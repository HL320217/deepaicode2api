package store

import "deepaicode2api/internal/egress"

func (s *Store) Egress() (egress.Config, egress.State) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return egress.Defaults(s.st.EgressConfig), egress.CloneState(s.st.EgressState)
}

func (s *Store) SaveEgress(c egress.Config, state egress.State) error {
	return s.resourceTransaction(func(st *State) error { st.EgressConfig = c; st.EgressState = egress.CloneState(state); return nil })
}
