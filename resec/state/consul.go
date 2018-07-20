package state

// Consul state used by the reconciler to decide what actions to take
type Consul struct {
	Ready      bool
	Healthy    bool
	LockIsHeld bool
	MasterAddr string
	MasterPort int
	Stopped    bool
}
