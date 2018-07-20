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

// reconciler will take a stream of changes happening to
// consul and redis and decide what actions that should be taken
type Reconciler struct {
	consul                 state.Consul          // Latest (cached) Consul state
	consulCommandCh        chan<- consul.Command // Write-only channel to request Consul actions to be taken
	consulStateCh          <-chan state.Consul   // Read-only channel to get Consul state updates
	logger                 *log.Entry            // reconciler logger
	forceReconcileInterval time.Duration         // How often we should force reconcile
	reconcileInterval      time.Duration         // how often we should evaluate our state
	redis                  state.Redis           // Latest (cached) Redis state
	redisCommandCh         chan<- redis.Command  // Write-only channel to request Redis actions to be taken
	redisStateCh           <-chan state.Redis    // Read-only channel to get Redis state updates
	reconcile              bool                  // Used to track if any state has been updated
	signalCh               chan os.Signal        // signal channel (OS / signal shutdown)
	stopCh                 chan interface{}      // stop channel (internal shutdown)s
}

// sendRedisCommand will build and send a Redis command
func (r *Reconciler) sendRedisCommand(cmd redis.CommandName) {
	r.redisCommandCh <- redis.NewCommand(cmd, r.consul)
}

// sendConsulCommand will build and send a Consul command
func (r *Reconciler) sendConsulCommand(cmd consul.CommandName) {
	r.consulCommandCh <- consul.NewCommand(cmd, r.redis)
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
			// No new state since last, doing nothing
			if r.reconcile == false {
				continue
			}
			r.reconcile = false

			// do we have the initial state to start reconciliation
			if r.missingInitialState() {
				r.logger.Debug("Not ready to reconcile yet, missing initial state")
				continue
			}

			// If Consul is not healthy, we can't make changes to the topology, as
			// we are unable to update the Consul catalog
			if r.consul.IsUnhealhy() {
				r.logger.Debugf("Can't reconcile, Consul is not healthy")
				continue
			}

			// If Redis is not healthy, we can't re-configure Redis if need be, so the only
			// option is to step down as leader (if we are) and remove our Consul service
			if r.redis.IsUnhealthy() {
				r.logger.Debugf("Redis is not healthy, deregister Consul service and don't do any further changes")
				r.sendConsulCommand(consul.ReleaseLockCommand)
				r.sendConsulCommand(consul.DeregisterServiceCommand)
				continue
			}

			// if the Consul Lock is held (aka this instance should be master)
			if r.consul.IsMaster() {

				// redis is already configured as master, so just update the consul check
				if r.redis.IsRedisMaster() {
					r.logger.Debug("We are already Consul Master and we run as Redis Master")
					r.sendConsulCommand(consul.UpdateServiceCommand)
					continue
				}

				// redis is not currently configured as master
				r.logger.Info("Configure Redis as master")
				r.sendRedisCommand(redis.RunAsMasterCommand)
				r.sendConsulCommand(consul.RegisterServiceCommand)
				continue
			}

			// if the consul lock is *not* held (aka this instance should be slave)
			if r.consul.IsSlave() {

				// can't enslave if there are no known master redis in consul catalog
				if r.consul.NoMasterElected() {
					r.logger.Warn("Currently no master Redis is elected in Consul catalog, can't enslave local Redis")
					continue
				}

				// is slave, and following the current master
				// TODO(jippi): consider replication lag
				if r.isSlaveOfCurrentMaster() {
					r.logger.Debug("We are *not* consul master and correctly enslaved to current master")
					r.sendConsulCommand(consul.UpdateServiceCommand)
					continue
				}

				// is slave, but not slave of current master
				r.logger.Info("Reconfigure Redis as slave")
				r.sendRedisCommand(redis.RunAsSlaveCommand)
				r.sendConsulCommand(consul.RegisterServiceCommand)
				continue
			}
		}
	}
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
			r.redis = redis
			r.reconcile = true
			f.Reset(r.forceReconcileInterval)

		// New Consul state change
		case consul, ok := <-r.consulStateCh:
			if !ok {
				r.logger.Error("Consul master service channel was closed, shutting down")
				return
			}

			r.logger.Debug("New Consul state")
			r.consul = consul
			r.reconcile = true
			f.Reset(r.forceReconcileInterval)
		}
	}
}

// missingInitialState return whether we got initial state from both Consul
// and Redis, so we are able to start making decissions on the state of
// the Redis under management
func (r *Reconciler) missingInitialState() bool {
	if r.redis.Ready == false {
		r.logger.Warn("Redis still missing initial state")
		return true
	}

	if r.consul.Ready == false {
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
	if r.redis.IsRedisMaster() {
		logger.Debugf("isRedismaster() == true")
		return false
	}

	// if the host don't match consul state, it's not slave (of the right node)
	if r.redis.Replication.MasterHost != r.consul.MasterAddr {
		logger.Debugf("'master_host=%s' do not match expected master host %s", r.redis.Replication.MasterHost, r.consul.MasterAddr)
		return false
	}

	// if the port don't match consul state, it's not slave (of the right node)
	if r.redis.Replication.MasterPort != r.consul.MasterPort {
		logger.Debugf("'master_port=%d' do not match expected master host %d", r.redis.Replication.MasterPort, r.consul.MasterPort)
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
			if r.redis.Stopped && redisStopped == false {
				redisStopped = true
				wg.Done()
			}

			if r.consul.Stopped && consulStopped == false {
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
