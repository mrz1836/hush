package token

func (s *memStore) Revoke(jti string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[jti] = struct{}{}
	delete(s.live, jti)
	return nil
}
