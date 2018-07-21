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
	ResultSkip             = Result("skip")
	ResultMissingState     = Result("missing_state")
	ResultConsulNotHealthy = Result("consul_not_healthy")
	ResultRedisNotHealthy  = Result("redis_not_healthy")
	ResultUpdateService    = Result("consul_update_service")
	ResultRunAsMaster      = Result("run_as_master")
	ResultRunAsSlave       = Result("run_as_slave")
	ResultNoMasterElected  = Result("no_master_elected")
	ResultUnknown          = Result("unknown")
)

type Result string

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

func (r *Reconciler) evaluate() Result {
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

		// is slave, and following the current master
		// TODO(jippi): consider replication lag
		if r.isSlaveOfCurrentMaster() {
			r.logger.Debug("We are *not* consul master and correctly enslaved to current master")
			r.sendConsulCommand(consul.UpdateServiceCommand)
			return ResultUpdateService
		}

		// is slave, but not slave of current master
		r.logger.Info("Reconfigure Redis as slave")
		r.sendRedisCommand(redis.RunAsSlaveCommand)
		r.sendConsulCommand(consul.RegisterServiceCommand)
		return ResultRunAsSlave
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

// isSlaveOfCurrentMaster return wheter the Redis under management currently
// are configured to be slave of the currently elected master Redis
func (r *Reconciler) isSlaveOfCurrentMaster() bool {
	logger := r.logger.WithField("check", "isSlaveOfCurrentMaster")
	// if Redis thing its master, it can't be a slave of another node
	if r.redisState.IsRedisMaster() {
		logger.Debugf("isRedismaster() == true")
		return false
	}

	// if the host don't match consul state, it's not slave (of the right node)
	if r.redisState.Replication.MasterHost != r.consulState.MasterAddr {
		logger.Debugf("'master_host=%s' do not match expected master host %s", r.redisState.Replication.MasterHost, r.consulState.MasterAddr)
		return false
	}

	// if the port don't match consul state, it's not slave (of the right node)
	if r.redisState.Replication.MasterPort != r.consulState.MasterPort {
		logger.Debugf("'master_port=%d' do not match expected master host %d", r.redisState.Replication.MasterPort, r.consulState.MasterPort)
		return false
	}

	// looks good
	return true
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
