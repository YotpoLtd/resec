// +test

package reconciler

import (
	"testing"

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
		withConsulState(state.Consul{Ready: true, Healthy: true, Master: false}).
		eval(ResultMissingState)

	// redis updated state, connected but no known replication status
	helper.
		withRedis(state.Redis{Ready: true, Connected: true}).
		eval(ResultNoMasterElected)

	// consul updated state, we know hold the master lock, so configure redis to become master
	helper.
		withConsulState(state.Consul{Ready: true, Healthy: true, Master: true}).
		expectConsulCommands("register_service").
		expectRedisCommands("run_as_master").
		eval(ResultRunAsMaster)

	// redis updated state, its now running as a master node, so all we need to do is update the service
	helper.
		withRedis(state.Redis{Ready: true, Connected: true, Replication: state.RedisReplicationState{Role: "master"}}).
		expectConsulCommands("update_service").
		eval(ResultUpdateService)

	// no state change, so all we need to do is update the service
	helper.
		expectConsulCommands("update_service").
		eval(ResultUpdateService)
}
