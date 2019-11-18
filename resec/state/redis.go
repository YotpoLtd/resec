package state

import (
	"time"
)

// Redis state represent the full state of the connection with Redis
type Redis struct {
	Healthy    bool        // are we able to connect to Redis?
	Ready      bool        // are we ready to provide state for the reconciler?
	Info       RedisStatus // parse "info" data
	InfoString string      `testdiff:"ignore"` // raw "info" data
	Stopped    bool
}

// isRedisMaster return whether the Redis under management currently
// see itself as a master instance or not
func (r *Redis) IsRedisMaster() bool {
	return r.Info.Role == "master"
}

func (r *Redis) IsUnhealthy() bool {
	return r.Healthy == false
}

type RedisStatus struct {
	Role                 string        // current redis role (master or slave)
	Loading              bool          // is redis currently loading data from disk?
	MasterLinkUp         bool          // is the link to master up (master_link_status == up)
	MasterLinkDownSince  time.Duration // for how long has the master link been down?
	MasterSyncInProgress bool          // is a master sync in progress?
	MasterHost           string        // if slave, the master hostname its replicating from
	MasterPort           int           // if slave, the master port its replicating from
}

// changed will test if the current replication state is different from
// the new one passed in as argument
func (r *RedisStatus) Changed(new RedisStatus) bool {
	if r.Role != new.Role {
		return true
	}

	if r.MasterHost != new.MasterHost {
		return true
	}

	if r.MasterPort != new.MasterPort {
		return true
	}

	if r.MasterLinkUp != new.MasterLinkUp {
		return true
	}

	if r.MasterSyncInProgress != new.MasterSyncInProgress {
		return true
	}

	if r.Loading != new.Loading {
		return true
	}

	return false
}
