package redis

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/YotpoLtd/resec/resec/state"
)

// emit will send a state update to the reconciler
func (m *Manager) emit() {
	m.stateCh <- *m.state
}

// runAsMaster sets the instance to be the master
func (m *Manager) runAsMaster() error {
	if err := m.client.SlaveOf("no", "one").Err(); err != nil {
		return err
	}

	m.logger.Info("Promoted redis to Master")
	return nil
}

// runAsSlave sets the instance to be a slave for the master
func (m *Manager) runAsSlave(masterAddress string, masterPort int) error {
	m.logger.Infof("Enslaving redis %s to be slave of %s:%d", m.config.Address, masterAddress, masterPort)

	if err := m.client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err(); err != nil {
		return fmt.Errorf("Could not enslave redis %s to be slave of %s:%d (%v)", m.config.Address, masterAddress, masterPort, err)
	}

	m.logger.Infof("Enslaved redis %s to be slave of %s:%d", m.config.Address, masterAddress, masterPort)
	return nil
}

func (m *Manager) cleanup() {
	close(m.stopCh)

	m.state.Stopped = true
	m.emit()
}

func (m *Manager) start() {
	go m.watchReplicationStatus()
	m.waitForRedisToBeReady()
}

func (m *Manager) CommandRunner() {
	for {
		select {

		case <-m.stopCh:
			return

		case payload := <-m.commandCh:
			switch payload.name {
			case StartCommand:
				m.start()

			case StopCommand:
				m.cleanup()

			case RunAsMasterCommand:
				m.runAsMaster()

			case RunAsSlaveCommand:
				m.runAsSlave(payload.consulState.MasterAddr, payload.consulState.MasterPort)
			}
		}
	}
}

// watchReplicationStatus checks redis replication status
func (m *Manager) watchReplicationStatus() {
	ticker := time.NewTicker(time.Second)

	for ; true; <-ticker.C {
		result, err := m.client.Info("replication").Result()
		// any failure will trigger a disconnect event
		if err != nil {
			m.state.Healthy = false
			m.emit()

			m.logger.Errorf("Can't connect to redis: %+v", err)
			continue
		}

		// if we previously was disconnected, but now succeded again, emit a (re)connected event
		if m.state.Healthy == false {
			m.state.Healthy = true
			m.emit()
		}

		kvPair := m.parseKeyValue(result)

		// Create new replication state
		replicationState := state.RedisReplicationState{
			Role: kvPair["role"],
		}

		// Add master_host to state (if set) - only available for slaves
		if masterHost, ok := kvPair["master_host"]; ok {
			replicationState.MasterHost = masterHost
		}

		// Add master_port to state (if set) - only available for slaves
		if masterPortString, ok := kvPair["master_port"]; ok {
			masterPort, err := strconv.Atoi(masterPortString)
			if err == nil {
				replicationState.MasterPort = masterPort
			}
		}

		// compare current and new state, if no changes, don't publish
		// a new state to the reconciler
		if replicationState.Changed(m.state.Replication) == false {
			continue
		}

		m.state.Replication = replicationState
		m.state.ReplicationString = result
		m.emit()
	}
}

// waitForRedisToBeReady will check if we got the initial redis state we need
// for the reconciler to do its job right out of the box
func (m *Manager) waitForRedisToBeReady() {
	t := time.NewTicker(500 * time.Millisecond)

	for ; true; <-t.C {
		// if we got replication data from redis, we are ready
		if m.state.Replication.Role != "" {
			m.state.Ready = true
			m.emit()

			return
		}
	}
}

func (m *Manager) parseKeyValue(str string) map[string]string {
	res := make(map[string]string)

	lines := strings.Split(str, "\r\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}

		pair := strings.Split(line, ":")
		if len(pair) != 2 {
			continue
		}

		res[pair[0]] = pair[1]
	}

	return res
}

func (m *Manager) Config() Config {
	return *m.config
}

func (m *Manager) StateChReader() <-chan state.Redis {
	return m.stateCh
}

func (m *Manager) CommandChWriter() chan<- Command {
	return m.commandCh
}
