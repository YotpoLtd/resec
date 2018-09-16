package state

// Consul state used by the reconciler to decide what actions to take
type Consul struct {
	Ready      bool
	Healthy    bool
	Master     bool
	MasterAddr string
	MasterPort int
	Stopped    bool
}

// NoMasterElected return whether any Consul elected Redis master exist
func (c *Consul) NoMasterElected() bool {
	return c.MasterAddr == "" && c.MasterPort == 0
}

func (c *Consul) IsUnhealhy() bool {
	return c.Healthy == false
}

// isConsulMaster return whether the Consul lock is held or not
// if it's held, the Redis under management should become master
func (c *Consul) IsMaster() bool {
	return c.Master
}

// isConsulSlave return whether the Consul lock is held or not
// if its *not* hold, the Reids under management should become slave
func (c *Consul) IsSlave() bool {
	return c.Master == false
}
