package state

// Redis state represent the full state of the connection with Redis
type Redis struct {
	Connected         bool                  // are we able to connect to Redis?
	Ready             bool                  // are we ready to provide state for the reconciler?
	Replication       RedisReplicationState // current replication data
	ReplicationString string                // raw replication info
	Stopped           bool
}

type RedisReplicationState struct {
	Role       string // current redis role (master or slave)
	MasterHost string // if slave, the master hostname its replicating from
	MasterPort int    // if slave, the master port its replicating from
}

// changed will test if the current replication state is different from
// the new one passed in as argument
func (r *RedisReplicationState) Changed(new RedisReplicationState) bool {
	if r.Role != new.Role {
		return true
	}

	if r.MasterHost != new.MasterHost {
		return true
	}

	if r.MasterPort != new.MasterPort {
		return true
	}

	return false
}
