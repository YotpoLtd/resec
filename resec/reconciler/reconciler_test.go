// +test

package reconciler

import (
	"testing"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	"github.com/YotpoLtd/resec/resec/state"
)

func TestReconciler_RunBecomeMaster(t *testing.T) {
	helper := newTestReconsiler(t)
	helper.consume()
	defer helper.stop()

	// initial state, reconsiler can't do any work because it lacks state
	helper.
		eval(ResultMissingState)

	// consul updated state, but do not have master lock
	helper.
		withConsulState(state.Consul{
			Ready:   true,
			Healthy: true,
			Master:  false,
		}).
		eval(ResultMissingState)

	// redis updated state, connected but no known replication status
	helper.
		withRedisState(state.Redis{
			Ready:   true,
			Healthy: true,
		}).
		eval(ResultNoMasterElected)

	// consul updated state, we know hold the master lock, so configure redis to become master
	helper.
		withConsulState(state.Consul{
			Ready:   true,
			Healthy: true,
			Master:  true,
		}).
		expectConsulCommands(
			consul.RegisterServiceCommand,
		).
		expectRedisCommands(
			redis.RunAsMasterCommand,
		).
		eval(ResultRunAsMaster)

	// redis updated state, its now running as a master node, so all we need to do is update the service
	helper.
		withRedisState(state.Redis{
			Ready:   true,
			Healthy: true,
			Replication: state.RedisReplicationState{
				Role: "master",
			},
		}).
		expectConsulCommands(
			consul.UpdateServiceCommand,
		).
		eval(ResultUpdateService)

	// no state change, so all we need to do is update the service
	helper.
		expectConsulCommands(
			consul.UpdateServiceCommand,
		).
		eval(ResultUpdateService)
}

func TestReconciler_UnhealthyConsul(t *testing.T) {
	helper := newTestReconsiler(t)
	helper.consume()
	defer helper.stop()

	// with local Redis healthy, and local Consul unhealthy,
	// the reconsiler should not do any work at all
	helper.
		withRedisState(state.Redis{
			Ready:   true,
			Healthy: true,
		}).
		withConsulState(state.Consul{
			Ready:   true,
			Healthy: false,
		}).
		eval(ResultConsulNotHealthy)
}

func TestReconciler_UnhealthyRedis(t *testing.T) {
	helper := newTestReconsiler(t)
	helper.consume()
	defer helper.stop()

	// with local Consul healthy, and local Redis unhealthy,
	// the reconsiler should give up the consul lock (if held) and deregister the service
	helper.
		withConsulState(state.Consul{
			Ready:   true,
			Healthy: true,
		}).
		withRedisState(state.Redis{
			Ready:   true,
			Healthy: false,
		}).
		expectConsulCommands(
			consul.ReleaseLockCommand,
			consul.DeregisterServiceCommand,
		).
		eval(ResultRedisNotHealthy)
}

func TestReconciler_SlaveNoMasterElected(t *testing.T) {
	helper := newTestReconsiler(t)
	helper.consume()
	defer helper.stop()

	// with local Consul and local Redis healthy, but no cluster elected Consul master
	// the reconsiler should do no work
	helper.
		withConsulState(state.Consul{
			Ready:   true,
			Healthy: true,
		}).
		withRedisState(state.Redis{
			Ready:   true,
			Healthy: true,
		}).
		eval(ResultNoMasterElected)
}

func TestReconciler_SlaveMasterElected(t *testing.T) {
	helper := newTestReconsiler(t)
	helper.consume()
	defer helper.stop()

	// with local Consul and Redis healthy but not elected Consul master
	// and a remote Consul master, local redis should be enslaved to the remote master
	helper.
		withConsulState(state.Consul{
			Ready:      true,
			Healthy:    true,
			MasterAddr: "127.0.0.1",
			MasterPort: 6379,
		}).
		withRedisState(state.Redis{
			Ready:   true,
			Healthy: true,
		}).
		expectRedisCommands(
			redis.RunAsSlaveCommand,
		).
		eval(ResultRunAsSlave)
}

func TestReconciler_SlaveMasterElectedAlready(t *testing.T) {
	helper := newTestReconsiler(t)
	helper.consume()
	defer helper.stop()

	// with local Consul and Redis healthy, but not elected Consul master
	// and a remote Consul master, which Redis is already enslaved to
	// the reconsiler should only update the Consul service
	helper.
		withConsulState(state.Consul{
			Ready:      true,
			Healthy:    true,
			MasterAddr: "127.0.0.1",
			MasterPort: 6379,
		}).
		withRedisState(state.Redis{
			Ready:   true,
			Healthy: true,
			Replication: state.RedisReplicationState{
				MasterHost: "127.0.0.1",
				MasterPort: 6379,
			},
		}).
		expectConsulCommands(
			consul.UpdateServiceCommand,
		).
		eval(ResultUpdateService)
}
