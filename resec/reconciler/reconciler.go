package reconciler

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	"github.com/YotpoLtd/resec/resec/state"
	"github.com/bep/debounce"
	log "github.com/sirupsen/logrus"
	"gopkg.in/d4l3k/messagediff.v1"
)

const (
	ResultConsulNotHealthy     = resultType("consul_not_healthy")
	ResultMasterLinkDown       = resultType("master_link_down")
	ResultMasterSyncInProgress = resultType("master_sync_in_progress")
	ResultMissingState         = resultType("missing_state")
	ResultNoMasterElected      = resultType("no_master_elected")
	ResultRedisNotHealthy      = resultType("redis_not_healthy")
	ResultRunAsMaster          = resultType("run_as_master")
	ResultRunAsSlave           = resultType("run_as_slave")
	ResultSkip                 = resultType("skip")
	ResultUnknown              = resultType("unknown")
	ResultUpdateService        = resultType("consul_update_service")
)

type resultType string

// reconciler will take a stream of changes happening to
// consul and redis and decide what actions that should be taken
type Reconciler struct {
	consulCommandCh        chan<- consul.Command // Write-only channel to request Consul actions to be taken
	consulState            state.Consul          // Latest (cached) Consul state
	consulStateCh          <-chan state.Consul   // Read-only channel to get Consul state updates
	debugSignalCh          chan os.Signal        // signal channel (OS / signal debug state)
	forceReconcileInterval time.Duration         // How often we should force reconcile
	logger                 *log.Entry            // reconciler logger
	reconcileCh            chan interface{}      // Channel to trigger a reconcile loop
	redisCommandCh         chan<- redis.Command  // Write-only channel to request Redis actions to be taken
	redisState             state.Redis           // Latest (cached) Redis state
	redisStateCh           <-chan state.Redis    // Read-only channel to get Redis state updates
	signalCh               chan os.Signal        // signal channel (OS / signal shutdown)
	stopCh                 chan interface{}      // stop channel (internal shutdown)
	sync.Mutex
}

// sendRedisCommand will build and send a Redis command
func (r *Reconciler) sendRedisCommand(cmd redis.CommandName) {
	r.redisCommandCh <- redis.NewCommand(cmd, r.consulState)
}

// sendConsulCommand will build and send a Consul command
func (r *Reconciler) sendConsulCommand(cmd consul.CommandName) {
	r.consulCommandCh <- consul.NewCommand(cmd, r.redisState)
}

// Run starts the procedure
func (r *Reconciler) Run() {
	// Default state
	currentState := ResultUnknown

	// Configure logger
	r.logger = log.WithField("system", "reconciler").WithField("state", currentState)

	// Fire up our internal state reader, consuming updates from Consul and Redis
	go r.stateReader()

	// Start the Consul reader
	r.sendConsulCommand(consul.StartCommand)

	// Start the Redis reader
	r.sendRedisCommand(redis.StartCommand)

	// how long to wait between forced renconcile (e.g. to keep TTL happy)
	f := time.NewTimer(r.forceReconcileInterval)

	// Debounce reconciler update events if they happen in rapid succession
	debounced := debounce.New(100 * time.Millisecond)

	for {
		select {
		// signal handler
		case <-r.signalCh:
			fmt.Println("")
			r.logger.Warn("Caught signal, stopping reconciler loop")
			go r.stop()

		case sig := <-r.debugSignalCh:
			if sig == syscall.SIGUSR1 {
				r.logger.WithField("dump_state", "consul").Warn(r.prettyPrint(r.consulState))
				r.logger.WithField("dump_state", "redis").Warn(r.prettyPrint(r.redisState))
			}

			if sig == syscall.SIGUSR2 {
				r.logger.WithField("dump_state", "reconciler").Warn(r.prettyPrint(r))
			}

		// we fake state change to ensure we reconcile periodically
		case <-f.C:
			r.reconcileCh <- true
			f.Reset(r.forceReconcileInterval)

		// stop the infinite loop
		case <-r.stopCh:
			r.logger.Info("Shutdown requested, stopping reconciler loop")
			return

		// evaluate state and reconcile the systems under control
		case <-r.reconcileCh:
			debounced(func() {
				newState := r.evaluate()

				// If there are no state change, we're done
				if currentState == newState {
					return
				}

				// Update the reconciler logger to reflect the new state
				r.logger = r.logger.WithField("state", newState)

				// Log the reconciler state change
				r.logger.
					WithField("old_state", currentState).
					Infof("Reconciler state transitioned from '%s' to '%s'", currentState, newState)

				// Update current state
				currentState = newState
			})
		}
	}
}

func (r *Reconciler) evaluate() resultType {
	// Make sure the state doesn't change half-way through our evaluation
	r.Lock()
	defer r.Unlock()

	defer r.timeTrack(time.Now(), "reconciler")

	// do we have the initial state to start reconciliation
	if r.missingInitialState() {
		r.logger.Debug("Not ready to reconcile yet, missing initial state")
		return ResultMissingState
	}

	// If Consul is not healthy, we can't make changes to the topology, as
	// we are unable to update the Consul catalog
	if r.consulState.IsUnhealhy() {
		r.logger.Debugf("Can't reconcile, Consul is not healthy")
		return ResultConsulNotHealthy
	}

	// If Redis is not healthy, we can't re-configure Redis if need be, so the only
	// option is to step down as leader (if we are) and remove our Consul service
	if r.redisState.IsUnhealthy() {
		r.logger.Debugf("Redis is not healthy, deregister Consul service and don't do any further changes")
		r.sendConsulCommand(consul.ReleaseLockCommand)
		r.sendConsulCommand(consul.DeregisterServiceCommand)
		return ResultRedisNotHealthy
	}

	// if the Consul Lock is held (aka this instance should be master)
	if r.consulState.IsMaster() {

		// redis is already configured as master, so just update the consul check
		if r.redisState.IsRedisMaster() {
			r.logger.Debug("We are already Consul Master and we run as Redis Master")
			r.sendConsulCommand(consul.UpdateServiceCommand)
			return ResultUpdateService
		}

		// redis is not currently configured as master
		r.logger.Info("Configure Redis as master")
		r.sendRedisCommand(redis.RunAsMasterCommand)
		r.sendConsulCommand(consul.RegisterServiceCommand)
		return ResultRunAsMaster
	}

	// if the consul lock is *not* held (aka this instance should be slave)
	if r.consulState.IsSlave() {

		// can't enslave if there are no known master redis in consul catalog
		if r.consulState.NoMasterElected() {
			r.logger.Warn("Currently no master Redis is elected in Consul catalog, can't enslave local Redis")
			return ResultNoMasterElected
		}

		// is not following the current Redis master
		if r.notSlaveOfCurrentMaster() {
			r.logger.Info("Reconfigure Redis as slave")
			r.sendRedisCommand(redis.RunAsSlaveCommand)
			return ResultRunAsSlave
		}

		// if sycing with redis master, lets wait for it to complete
		if r.isMasterSyncInProgress() {
			r.logger.Warn("Master sync in progress, can't serve traffic")
			r.sendConsulCommand(consul.DeregisterServiceCommand)
			return ResultMasterSyncInProgress
		}

		// if master link is down, lets wait for it to come back up
		if r.isMasterLinkDown() && r.isMasterLinkDownTooLong() {
			r.logger.Warn("Master link is down, can't serve traffic")
			r.sendConsulCommand(consul.DeregisterServiceCommand)
			return ResultMasterLinkDown
		}

		//
		// TODO(jippi): consider replication lag
		//

		// everything is fine, update service ttl
		r.logger.Debug("We are *not* consul master and correctly enslaved to current master")
		r.sendConsulCommand(consul.UpdateServiceCommand)
		return ResultUpdateService
	}

	return ResultUnknown
}

func (r *Reconciler) stateReader() {
	for {
		select {
		// stop the infinite loop
		case <-r.stopCh:
			r.logger.Info("Shutdown requested, stopping state loop")
			return

		// New redis state change
		case redis, ok := <-r.redisStateCh:
			if !ok {
				r.logger.Error("Redis state channel was closed, shutting down")
				return
			}

			r.Lock()
			r.logger.Debug("New Redis state")
			changed := r.diffState(r.redisState, redis)
			if changed {
				r.redisState = redis
				r.reconcileCh <- true
			}
			r.Unlock()

		// New Consul state change
		case consul, ok := <-r.consulStateCh:
			if !ok {
				r.logger.Error("Consul state channel was closed, shutting down")
				return
			}

			r.Lock()
			r.logger.Debug("New Consul state")
			changed := r.diffState(r.consulState, consul)
			if changed {
				r.consulState = consul
				r.reconcileCh <- true
			}
			r.Unlock()
		}
	}
}

// isMasterSyncInProgress return whether the slave is currently doing a full sync from
// the redis master - this is also the initial sync triggered by doing a SLAVEOF command
func (r *Reconciler) isMasterSyncInProgress() bool {
	return r.redisState.Info.MasterSyncInProgress
}

// isMasterLinkDown return whether the slave has lost connection to the
// redis master
func (r *Reconciler) isMasterLinkDown() bool {
	return r.redisState.Info.MasterLinkUp == false
}

// isMasterLinkDownTooLong return whether the slave has lost connectity to the
// redis master for too long
func (r *Reconciler) isMasterLinkDownTooLong() bool {
	// TODO(jippi): make 10s configurable
	return r.redisState.Info.MasterLinkDownSince > 10*time.Second
}

// missingInitialState return whether we got initial state from both Consul
// and Redis, so we are able to start making decissions on the state of
// the Redis under management
func (r *Reconciler) missingInitialState() bool {
	if r.redisState.Ready == false {
		r.logger.Warn("Redis still missing initial state")
		return true
	}

	if r.consulState.Ready == false {
		r.logger.Warn("Consul still missing initial state")
		return true
	}

	return false
}

// notSlaveOfCurrentMaster return wheter the Redis under management currently
// are configured to be slave of the currently elected master Redis
func (r *Reconciler) notSlaveOfCurrentMaster() bool {
	logger := r.logger.WithField("check", "isSlaveOfCurrentMaster")
	// if Redis thing its master, it can't be a slave of another node
	if r.redisState.IsRedisMaster() {
		logger.Debugf("isRedismaster() == true")
		return true
	}

	// if the host don't match consul state, it's not slave (of the right node)
	if r.redisState.Info.MasterHost != r.consulState.MasterAddr {
		logger.Debugf("'master_host=%s' do not match expected master host %s", r.redisState.Info.MasterHost, r.consulState.MasterAddr)
		return true
	}

	// if the port don't match consul state, it's not slave (of the right node)
	if r.redisState.Info.MasterPort != r.consulState.MasterPort {
		logger.Debugf("'master_port=%d' do not match expected master host %d", r.redisState.Info.MasterPort, r.consulState.MasterPort)
		return true
	}

	// looks good
	return false
}

// stop will ensure Consul and Redis will gracefully stop
func (r *Reconciler) stop() {
	var wg sync.WaitGroup

	wg.Add(3)

	r.logger.Debugf("Consul Cleanup started ")
	r.sendConsulCommand(consul.StopConsulCommand)

	r.logger.Debugf("Redis Cleanup started ")
	r.sendRedisCommand(redis.StopCommand)

	// monitor cleanup process from state
	go func() {
		redisStopped := false
		consulStopped := false

		for {
			if r.redisState.Stopped && redisStopped == false {
				redisStopped = true
				wg.Done()
			}

			if r.consulState.Stopped && consulStopped == false {
				consulStopped = true
				wg.Done()
			}

			if redisStopped && consulStopped {
				wg.Done()
				return
			}
		}
	}()

	wg.Wait()

	r.logger.Debugf("Cleanup completed")
	close(r.stopCh)
}

func (r *Reconciler) timeTrack(start time.Time, name string) {
	elapsed := time.Since(start)
	r.logger.Debugf("%s took %s", name, elapsed)
}

func (r *Reconciler) diffState(a, b interface{}) bool {
	d, equal := messagediff.DeepDiff(a, b)
	if equal {
		return false
	}

	for path, added := range d.Added {
		r.logger.Debugf("added: %s = %#v", path.String(), added)
	}
	for path, removed := range d.Removed {
		r.logger.Debugf("removed: %s = %#v", path.String(), removed)
	}
	for path, modified := range d.Modified {
		r.logger.Debugf("modified: %s = %#v", path.String(), modified)
	}

	return true
}

// prettyPrint will JSON encode the input and return the string
// we use it to print the internal state of the reconciler when getting
// the right SIG
func (r *Reconciler) prettyPrint(data interface{}) string {
	var p []byte
	p, err := json.MarshalIndent(data, "", "\t")
	if err != nil {
		r.logger.Error(err)
		return ""
	}

	return string(p)
}

// Marshalling to JSON is used when sendnig debug signal to the process
func (r *Reconciler) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"consulState":            r.consulState,
		"forceReconcileInterval": r.forceReconcileInterval,
		"redisState":             r.redisState,
	})
}
