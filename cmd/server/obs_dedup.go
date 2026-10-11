package main

// obsDedupKey identifies an observation within its transmission: the same
// observer reporting the same path is a duplicate.
type obsDedupKey struct{ observerID, pathJSON string }

// obsIndexThreshold is the observation count at which a transmission starts
// keeping maps for its duplicate and distinct-observer checks.
const obsIndexThreshold = 16

// hasObservation reports whether tx already holds an observation from
// observerID with pathJSON.
func (tx *StoreTx) hasObservation(observerID, pathJSON string) bool {
	return false
}

// addObservation appends obs to tx and keeps UniqueObserverCount and
// ObservationCount in step. Callers check hasObservation first.
func (tx *StoreTx) addObservation(obs *StoreObs) {
}
