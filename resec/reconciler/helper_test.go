// +test
package reconciler

import (
	"os"
	"testing"
	"time"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	"github.com/YotpoLtd/resec/resec/state"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

type helper struct {
	consulCommandCh        chan consul.Command
	consulCommandsExpected []string
	consulCommandsSeen     []consul.Command
	consulStateCh          chan state.Consul
	reconciler             *Reconciler
	redisCommandCh         chan redis.Command
	redisCommandsExpected  []string
	redisCommandsSeen      []redis.Command
	redisStateCh           chan state.Redis
	t                      *testing.T
}

// consume all consul/redis commands from the reconsiler
func (h *helper) consume() {
	go func() {
		for {
			select {
			case <-h.reconciler.stopCh:
				return
			case cmd := <-h.redisCommandCh:
				h.redisCommandsSeen = append(h.redisCommandsSeen, cmd)
			case cmd := <-h.consulCommandCh:
				h.consulCommandsSeen = append(h.consulCommandsSeen, cmd)
			}
		}
	}()
}

// withConsulState will change the reconsilers Consul state
func (h *helper) withConsulState(consulState state.Consul) *helper {
	h.reconciler.consulState = consulState
	return h
}

// withRedis will change the reconsilers Redis state
func (h *helper) withRedis(redisState state.Redis) *helper {
	h.reconciler.redisState = redisState
	return h
}

// expectRedisCommands sets the expected Redis commands we should see
// from the reconsiler during an evaluation
func (h *helper) expectRedisCommands(commands ...string) *helper {
	h.redisCommandsExpected = commands
	return h
}

// expectConsulCommands sets the expected Consul commands we should see
// from the reconsiler during an evaluation
func (h *helper) expectConsulCommands(commands ...string) *helper {
	h.consulCommandsExpected = commands
	return h
}

// eval will trigger a reconsiler evaluation and assert the result matches the
// configured expectations (expectRedisCommands / expectConsulCommands)
func (h *helper) eval(expectedResult Result) {
	h.reconciler.reconcile = true
	actualResult := h.reconciler.evaluate()

	assert.EqualValues(h.t, expectedResult, actualResult)
	time.Sleep(5 * time.Millisecond)

	assert.EqualValues(h.t, h.consulCommandsExpected, h.getConsulCommands())
	assert.EqualValues(h.t, h.redisCommandsExpected, h.getRedisCommands())

	h.reset()
}

// reset the internal assertions
func (h *helper) reset() {
	h.consulCommandsExpected = []string{}
	h.consulCommandsSeen = make([]consul.Command, 0)
	h.redisCommandsExpected = []string{}
	h.redisCommandsSeen = make([]redis.Command, 0)
}

// getConsulCommands will return the command name
// from the observed Consul commands triggered during
// an evaluation
func (h *helper) getConsulCommands() []string {
	r := make([]string, 0)

	for _, cmd := range h.consulCommandsSeen {
		r = append(r, cmd.Name())
	}

	return r
}

// getRedisCommands will return the command name
// from the observed Redis commands triggered during
// an evaluation
func (h *helper) getRedisCommands() []string {
	r := make([]string, 0)

	for _, cmd := range h.redisCommandsSeen {
		r = append(r, cmd.Name())
	}

	return r
}

// stop will stop the internal go-routines
func (h *helper) stop() {
	time.Sleep(5 * time.Millisecond)
	close(h.reconciler.stopCh)
}

// newTestReconsiler will create a new reconsiler for testing purposes
func newTestReconsiler(t *testing.T) *helper {
	consulCommandCh := make(chan consul.Command, 1)
	consulStateCh := make(chan state.Consul, 1)

	redisCommandCh := make(chan redis.Command, 1)
	redisStateCh := make(chan state.Redis, 1)

	r := &Reconciler{
		consulCommandCh:        consulCommandCh,
		consulStateCh:          consulStateCh,
		forceReconcileInterval: 5 * time.Millisecond,
		reconcileInterval:      time.Millisecond,
		redisCommandCh:         redisCommandCh,
		redisStateCh:           redisStateCh,
		signalCh:               make(chan os.Signal, 0),
		stopCh:                 make(chan interface{}, 0),
		logger:                 log.WithField("testing", true),
	}

	redisCommands := make([]redis.Command, 0)
	consulCommands := make([]consul.Command, 0)

	h := &helper{
		t:                  t,
		reconciler:         r,
		consulCommandCh:    consulCommandCh,
		consulStateCh:      consulStateCh,
		redisCommandCh:     redisCommandCh,
		redisStateCh:       redisStateCh,
		redisCommandsSeen:  redisCommands,
		consulCommandsSeen: consulCommands,
	}

	h.reset()
	return h
}
