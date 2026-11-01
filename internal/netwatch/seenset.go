package netwatch

// hashSet is a set of 64-bit hashes in one flat open-addressing table. It
// holds the watch's "already counted" keys: millions of them over a 72-hour
// window, where a Go map of uint64 costs ~26 bytes each and this ~11-16.
// Zero marks an empty slot, so a hash of zero is stored as one.
type hashSet struct {
	slots []uint64
	n     int
}

// add inserts h and reports whether it was new.
func (s *hashSet) add(h uint64) bool {
	if h == 0 {
		h = 1
	}
	if (s.n+1)*4 > len(s.slots)*3 {
		s.grow()
	}
	mask := uint64(len(s.slots) - 1)
	for i := h & mask; ; i = (i + 1) & mask {
		switch s.slots[i] {
		case h:
			return false
		case 0:
			s.slots[i] = h
			s.n++
			return true
		}
	}
}

func (s *hashSet) grow() {
	size := 2 * len(s.slots)
	if size < 64 {
		size = 64
	}
	old := s.slots
	s.slots = make([]uint64, size)
	mask := uint64(size - 1)
	for _, h := range old {
		if h == 0 {
			continue
		}
		i := h & mask
		for s.slots[i] != 0 {
			i = (i + 1) & mask
		}
		s.slots[i] = h
	}
}

func (s *hashSet) len() int { return s.n }
