package reconciler

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	"github.com/YotpoLtd/resec/resec/state"
	log "github.com/sirupsen/logrus"
)

var (
	wg sync.WaitGroup
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
	forceReconcileInterval time.Duration         // How often we should force reconcile
	logger                 *log.Entry            // reconciler logger
	reconcile              bool                  // Used to track if any state has been updated
	reconcileInterval      time.Duration         // how often we should evaluate our state
	redisCommandCh         chan<- redis.Command  // Write-only channel to request Redis actions to be taken
	redisState             state.Redis           // Latest (cached) Redis state
	redisStateCh           <-chan state.Redis    // Read-only channel to get Redis state updates
	signalCh               chan os.Signal        // signal channel (OS / signal shutdown)
	stopCh                 chan interface{}      // stop channel (internal shutdown)s
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
	r.logger = log.WithField("system", "reconciler")

	go r.stateReader()

	r.sendConsulCommand(consul.StartCommand)
	r.sendRedisCommand(redis.StartCommand)

	// how frequenty to reconcile when redis/consul state changes
	t := time.NewTicker(r.reconcileInterval)

	for {
		select {
		// signal handler
		case <-r.signalCh:
			fmt.Println("")
			r.logger.Info("Caught signal, stopping worker loop")
			go r.stop()

		// stop the infinite loop
		case <-r.stopCh:
			r.logger.Info("Shutdown requested, stopping reconciler loop")
			return

		case <-t.C:
			r.evaluate()
		}
	}
}

func (r *Reconciler) evaluate() resultType {
	// No new state since last, doing nothing
	if r.reconcile == false {
		return ResultSkip
	}
	r.reconcile = false

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

		// if master link is down, lets wait for it to come back up
		if r.isMasterLinkDown() && r.isMasterLinkDownTooLong() {
			r.logger.Warn("Master link is down, can't serve traffic")
			r.sendConsulCommand(consul.DeregisterServiceCommand)
			return ResultMasterLinkDown
		}

		// if sycing with redis master, lets wait for it to complete
		if r.isMasterSyncInProgress() {
			r.logger.Warn("Master sync in progress, can't serve traffic")
			r.sendConsulCommand(consul.DeregisterServiceCommand)
			return ResultMasterSyncInProgress
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
	// how long to wait between forced renconcile (e.g. to keep TTL happy)
	f := time.NewTimer(r.forceReconcileInterval)

	for {
		select {
		// stop the infinite loop
		case <-r.stopCh:
			r.logger.Info("Shutdown requested, stopping state loop")
			return

		// we fake state change to ensure we reconcile periodically
		case <-f.C:
			r.reconcile = true
			f.Reset(r.forceReconcileInterval)

		// New redis state change
		case redis, ok := <-r.redisStateCh:
			if !ok {
				r.logger.Error("Redis replication channel was closed, shutting down")
				return
			}

			r.logger.Debug("New Redis state")
			r.redisState = redis
			r.reconcile = true
			f.Reset(r.forceReconcileInterval)

		// New Consul state change
		case consul, ok := <-r.consulStateCh:
			if !ok {
				r.logger.Error("Consul master service channel was closed, shutting down")
				return
			}

			r.logger.Debug("New Consul state")
			r.consulState = consul
			r.reconcile = true
			f.Reset(r.forceReconcileInterval)
		}
	}
}

// isMasterSyncInProgress return whether the slave is currently doing a full sync from
// the redis master - this is also the initial sync triggered by doing a SLAVEOF command
func (r *Reconciler) isMasterSyncInProgress() bool {
	return r.redisState.Replication.MasterSyncInProgress
}

// isMasterLinkDown return whether the slave has lost connection to the
// redis master
func (r *Reconciler) isMasterLinkDown() bool {
	return r.redisState.Replication.MasterLinkUp == false
}

// isMasterLinkDownTooLong return whether the slave has lost connectity to the
// redis master for too long
func (r *Reconciler) isMasterLinkDownTooLong() bool {
	// TODO(jippi): make 10s configurable
	return r.redisState.Replication.MasterLinkDownSince > 10*time.Second
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
	if r.redisState.Replication.MasterHost != r.consulState.MasterAddr {
		logger.Debugf("'master_host=%s' do not match expected master host %s", r.redisState.Replication.MasterHost, r.consulState.MasterAddr)
		return true
	}

	// if the port don't match consul state, it's not slave (of the right node)
	if r.redisState.Replication.MasterPort != r.consulState.MasterPort {
		logger.Debugf("'master_port=%d' do not match expected master host %d", r.redisState.Replication.MasterPort, r.consulState.MasterPort)
		return true
	}

	// looks good
	return false
}

// stop will ensure consul and redis will gracefully stop
func (r *Reconciler) stop() {
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
