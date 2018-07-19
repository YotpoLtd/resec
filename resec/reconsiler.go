package resec

import (
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

// Reconsiler will take a stream of changes happening to
// consul and redis and decide what actions that should be taken
type reconsiler struct {
	logger           *log.Entry
	consulConnection *consulConnection // reference to the consul connection
	consulUpdateCh   chan consulState  // get updates from Consul manager
	consulState      consulState       // last seen consul state
	redisConnection  *redisConnection  // reference to the redis connection
	redisUpdateCh    chan redisState   // get updates from Redis manager
	redisState       redisState        // last seen redis state
	sigCh            chan os.Signal    // signal channel (OS / signal shutdown)
	stopCh           chan interface{}  // stop channel (internal shutdown)
}

// Run starts the procedure
func (r *reconsiler) Run() {
	r.logger = log.WithField("system", "reconsiler")

	r.redisUpdateCh = r.redisConnection.stateCh
	r.consulUpdateCh = r.consulConnection.stateCh

	r.redisConnection.start()
	r.consulConnection.start()

	t := time.NewTicker(100 * time.Millisecond)
	stateChanged := false

	for {
		select {
		case <-r.sigCh:
			fmt.Println("")
			r.logger.Info("Caught signal, stopping worker loop")
			go r.cleanup()

		case <-r.stopCh:
			r.logger.Info("Shutdown requested, stopping worker loop")
			return

		case redis, ok := <-r.redisUpdateCh:
			if !ok {
				r.logger.Error("Redis replication channel was closed, shutting down")
				return
			}

			r.logger.Debug("New Redis state")
			r.redisState = redis
			stateChanged = true

		case consul, ok := <-r.consulUpdateCh:
			if !ok {
				r.logger.Error("Consul master service channel was closed, shutting down")
				return
			}

			r.logger.Debug("New Consul state")
			r.consulState = consul
			stateChanged = true

		case <-t.C:
			// No new state since last, doing nothing
			if stateChanged == false {
				continue
			}
			stateChanged = false

			if r.isReadyToServe() == false {
				continue
			}

			// have consul lock and running as master == all good
			if r.isConsulMaster() && r.isRedisMaster() {
				r.logger.Debug("We are consul master *and* we run as Redis master")
				r.consulConnection.registerService(r.redisState)
				continue
			}

			// have consul lock, but not running as master == run as master
			if r.isConsulMaster() && r.isRedisMaster() == false {
				r.logger.Debug("We are consul master but *not* redis master")
				r.redisConnection.runAsMaster()
			}

			// no consul lock, but running as maste == run as slave
			if r.isConsulMaster() == false && r.isSlaveOfCurrentMaster() {
				r.logger.Debug("We are *not* consul master and not enslaved to current master")
				r.redisConnection.runAsSlave(r.consulState.masterAddr, r.consulState.masterPort)
			}
		}
	}
}

func (r *reconsiler) isConsulMaster() bool {
	return r.consulState.lockIsHeld
}

func (r *reconsiler) isRedisMaster() bool {
	return r.redisState.replication["role"] == "master"
}

func (r *reconsiler) isReadyToServe() bool {
	return r.redisState.ready && r.consulState.ready
}

func (r *reconsiler) isSlaveOfCurrentMaster() bool {
	// if Redis thing its master, it can't be a slave of another node
	if r.isRedisMaster() {
		return false
	}

	// if the replication field 'master_host' don't exist, can't be slave
	host, ok := r.redisState.replication["master_host"]
	if !ok {
		return false
	}

	// if the replication field 'master_port' don't exist, can't be slave
	port, ok := r.redisState.replication["master_port"]
	if !ok {
		return false
	}

	// if the host don't match consul state, it's not slave (of the right node)
	if host != r.consulState.masterAddr {
		return false
	}

	// if the port don't match consul state, it's not slave (of the right node)
	if port != string(r.consulState.masterPort) {
		return false
	}

	// looks good
	return true
}

func (r *reconsiler) cleanup() {
	r.logger.Debugf("Consul Cleanup started ")
	r.consulConnection.cleanup()

	r.logger.Debugf("Redis Cleanup started ")
	r.redisConnection.cleanup()

	r.logger.Debugf("Cleanup finished ")
	close(r.stopCh)
}
