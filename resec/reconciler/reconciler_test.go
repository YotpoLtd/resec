package reconciler

import (
	"os"
	"testing"
	"time"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	"github.com/YotpoLtd/resec/resec/state"
	"github.com/stretchr/testify/assert"
)

func TestReconciler_RunWithMaster(t *testing.T) {
	consulCommandCh := make(chan consul.Command, 1)
	consulStateCh := make(chan state.Consul, 1)

	redisCommandCh := make(chan redis.Command, 1)
	redisStateCh := make(chan state.Redis, 1)

	r := &Reconciler{
		consulCommandCh:   consulCommandCh,
		consulStateCh:     consulStateCh,
		reconcileInterval: 100 * time.Millisecond,
		redisCommandCh:    redisCommandCh,
		redisStateCh:      redisStateCh,
		signalCh:          make(chan os.Signal, 0),
		stopCh:            make(chan interface{}, 0),
	}

	redisCommands := make([]redis.Command, 0)
	consulCommands := make([]consul.Command, 0)

	go func() {
		for {
			select {
			case <-r.stopCh:
				return
			case cmd := <-redisCommandCh:
				redisCommands = append(redisCommands, cmd)
			case cmd := <-consulCommandCh:
				consulCommands = append(consulCommands, cmd)
			}
		}
	}()

	go r.Run()

	consulStateCh <- state.Consul{Ready: true, Healthy: true, LockIsHeld: true}
	redisStateCh <- state.Redis{Ready: true, Connected: true, Replication: state.RedisReplicationState{Role: "master"}}

	time.Sleep(500 * time.Millisecond)
	close(r.stopCh)

	// we only care for the first 3 events
	if len(consulCommands) > 3 {
		consulCommands = consulCommands[:3]
	}

	assert.Equal(t, []string{"start", "update_service", "update_service"}, listConsulCommands(consulCommands))
	assert.Len(t, redisCommands, 1)
	assert.Len(t, consulCommands, 3)
}

func listConsulCommands(commands []consul.Command) []string {
	r := make([]string, 0)

	for _, cmd := range commands {
		r = append(r, cmd.Name())
	}

	return r
}
