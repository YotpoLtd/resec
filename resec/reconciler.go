package resec

import (
	"fmt"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	wg sync.WaitGroup
)

// Reconsiler will take a stream of changes happening to
// consul and redis and decide what actions that should be taken
type reconciler struct {
	consulCommandCh   chan consulCommand // push updates to Consul manager
	consulState       consulState        // last seen consul state
	consulUpdateCh    chan consulState   // get updates from Consul manager
	logger            *log.Entry         // reconciler logger
	reconsileInterval time.Duration      // How often we should force reconsile
	redisCommand      chan redisCommand  // push updates to Consul manager
	redisState        redisState         // last seen redis state
	redisUpdateCh     chan redisState    // get updates from Redis manager
	shouldReconcile   bool               // Used to track if any state has been updated
	signalCh          chan os.Signal     // signal channel (OS / signal shutdown)
	stopCh            chan interface{}   // stop channel (internal shutdown)
}

// Run starts the procedure
func (r *reconciler) run() {
	r.logger = log.WithField("system", "reconsiler")

	go r.stateReader()
	r.consulCommandCh <- consulCommand{startConsulCommand, r.redisState}
	r.redisCommand <- redisCommand{startRedisCommand, r.consulState}

	// how frequenty to reconsile when redis/consul state changes
	t := time.NewTicker(100 * time.Millisecond)

	for {
		select {
		// signal handler
		case <-r.signalCh:
			fmt.Println("")
			r.logger.Info("Caught signal, stopping worker loop")
			go r.cleanup()

		// stop the infinite loop
		case <-r.stopCh:
			r.logger.Info("Shutdown requested, stopping reconsiler loop")
			return

		case <-t.C:
			// No new state since last, doing nothing
			if r.shouldReconcile == false {
				continue
			}
			r.shouldReconcile = false

			// do we have the initial state to start reconciliation
			if r.missingInitialState() {
				r.logger.Debug("Not ready to reconsile yet, missing initial state")
				continue
			}

			// If Consul is not healthy, we can't make changes to the topology, as
			// we are unable to update the Consul catalog
			if r.isConsulUnhealhy() {
				r.logger.Debugf("Can't reconsile, Consul is not healthy")
				continue
			}

			// If Redis is not healthy, we can't re-configure Redis if need be, so the only
			// option is to step down as leader (if we are) and remove our Consul service
			if r.isRedisUnhealthy() {
				r.logger.Debugf("Redis is not healthy, deregister Consul service and don't do any further changes")
				r.consulCommandCh <- consulCommand{releaseLockCommand, r.redisState}
				r.consulCommandCh <- consulCommand{deregisterServiceCommand, r.redisState}
				continue
			}

			// if the Consul Lock is held (aka this instance should be master)
			if r.isConsulMaster() {

				// redis is already configured as master, so just update the consul check
				if r.isRedisMaster() {
					r.logger.Debug("We are already Consul Master and we run as Redis Master")
					r.consulCommandCh <- consulCommand{updateServiceCommand, r.redisState}
					continue
				}

				// redis is not currently configured as master
				r.logger.Info("Configure Redis as master")
				r.redisCommand <- redisCommand{runAsMaster, r.consulState}
				r.consulCommandCh <- consulCommand{registerServiceCommand, r.redisState}
				continue
			}

			// if the consul lock is *not* held (aka this instance should be slave)
			if r.isConsulSlave() {

				// can't enslave if there are no known master redis in consul catalog
				if r.noMasterElected() {
					r.logger.Warn("Currently no master Redis is elected in Consul catalog, can't enslave local Redis")
					continue
				}

				// is slave, and following the current master
				// TODO(jippi): consider replication lag
				if r.isSlaveOfCurrentMaster() {
					r.logger.Debug("We are *not* consul master and correctly enslaved to current master")
					r.consulCommandCh <- consulCommand{updateServiceCommand, r.redisState}
					continue
				}

				// is slave, but not slave of current master
				r.logger.Info("Reconfigure Redis as slave")
				r.redisCommand <- redisCommand{runAsSlave, r.consulState}
				r.consulCommandCh <- consulCommand{registerServiceCommand, r.redisState}
				continue
			}
		}
	}
}

func (r *reconciler) stateReader() {
	// how long to wait between forced renconsile (e.g. to keep TTL happy)
	f := time.NewTimer(r.reconsileInterval)

	for {
		select {
		// stop the infinite loop
		case <-r.stopCh:
			r.logger.Info("Shutdown requested, stopping state loop")
			return

		// we fake state change to ensure we reconsile periodically
		case <-f.C:
			r.shouldReconcile = true
			f.Reset(r.reconsileInterval)

		// New redis state change
		case redis, ok := <-r.redisUpdateCh:
			if !ok {
				r.logger.Error("Redis replication channel was closed, shutting down")
				return
			}

			r.logger.Debug("New Redis state")
			r.redisState = redis
			r.shouldReconcile = true
			f.Reset(r.reconsileInterval)

		// New Consul state change
		case consul, ok := <-r.consulUpdateCh:
			if !ok {
				r.logger.Error("Consul master service channel was closed, shutting down")
				return
			}

			r.logger.Debug("New Consul state")
			r.consulState = consul
			r.shouldReconcile = true
			f.Reset(r.reconsileInterval)
		}
	}
}

func (r *reconciler) isConsulUnhealhy() bool {
	return r.consulState.healthy == false
}

func (r *reconciler) isRedisUnhealthy() bool {
	return r.redisState.connected == false
}

// isConsulMaster return whether the Consul lock is held or not
// if it's held, the Redis under management should become master
func (r *reconciler) isConsulMaster() bool {
	return r.consulState.lockIsHeld
}

// isConsulSlave return whether the Consul lock is held or not
// if its *not* hold, the Reids under management should become slave
func (r *reconciler) isConsulSlave() bool {
	return r.consulState.lockIsHeld == false
}

// isRedisMaster return whether the Redis under management currently
// see itself as a master instance or not
func (r *reconciler) isRedisMaster() bool {
	return r.redisState.replication.role == "master"
}

// noMasterElected return whether any Consul elected Redis master exist
func (r *reconciler) noMasterElected() bool {
	return r.consulState.masterAddr == "" && r.consulState.masterPort == 0
}

// missingInitialState return whether we got initial state from both Consul
// and Redis, so we are able to start making decissions on the state of
// the Redis under management
func (r *reconciler) missingInitialState() bool {
	if r.redisState.ready == false {
		r.logger.Warn("Redis still missing initial state")
		return true
	}

	if r.consulState.ready == false {
		r.logger.Warn("Consul still missing initial state")
		return true
	}

	return false
}

// isSlaveOfCurrentMaster return wheter the Redis under management currently
// are configured to be slave of the currently elected master Redis
func (r *reconciler) isSlaveOfCurrentMaster() bool {
	logger := r.logger.WithField("check", "isSlaveOfCurrentMaster")
	// if Redis thing its master, it can't be a slave of another node
	if r.isRedisMaster() {
		logger.Debugf("isRedismaster() == true")
		return false
	}

	// if the host don't match consul state, it's not slave (of the right node)
	if r.redisState.replication.masterHost != r.consulState.masterAddr {
		logger.Debugf("'master_host=%s' do not match expected master host %s", r.redisState.replication.masterHost, r.consulState.masterAddr)
		return false
	}

	// if the port don't match consul state, it's not slave (of the right node)
	if r.redisState.replication.masterPort != r.consulState.masterPort {
		logger.Debugf("'master_port=%d' do not match expected master host %d", r.redisState.replication.masterPort, r.consulState.masterPort)
		return false
	}

	// looks good
	return true
}

// cleanup will ensure consul and redis will gracefully shutdown
func (r *reconciler) cleanup() {
	wg.Add(2)

	r.logger.Debugf("Consul Cleanup started ")
	r.consulCommandCh <- consulCommand{stopConsulCommand, r.redisState}

	r.logger.Debugf("Redis Cleanup started ")
	r.redisCommand <- redisCommand{stopRedisCommand, r.consulState}

	wg.Wait()

	r.logger.Debugf("Cleanup finished ")
	close(r.stopCh)
}
