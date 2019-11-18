package redis

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis"
	"github.com/seatgeek/resec/resec/state"
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

	m.logger = m.logger.WithField("role", "master")
	m.logger.Info("Promoted redis to Master")
	return nil
}

// runAsSlave sets the instance to be a slave for the master
func (m *Manager) runAsSlave(masterAddress string, masterPort int) error {
	m.logger.Infof("Enslaving redis to be slave of %s:%d", masterAddress, masterPort)

	if err := m.client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err(); err != nil {
		return fmt.Errorf("Could not enslave redis to be slave of %s:%d (%v)", masterAddress, masterPort, err)
	}

	m.logger = m.logger.WithField("role", "slave")
	m.logger.Infof("Enslaved redis to be slave of %s:%d", masterAddress, masterPort)
	return nil
}

// disconnect all normal connections
func (m *Manager) disconnectUsers() error {
	m.logger.Warnf("Disconnecting all clients")
	cmd := redis.NewIntCmd("CLIENT", "KILL", "TYPE", "normal")
	m.client.Process(cmd)
	if err := cmd.Err(); err != nil {
		return err
	}

	m.logger.Warnf("Disconnected %d users", cmd.Val())
	return nil
}

func (m *Manager) cleanup() {
	close(m.stopCh)

	m.state.Stopped = true
	m.emit()
}

func (m *Manager) CommandRunner() {
	for {
		select {

		case <-m.stopCh:
			m.logger.Infof("Shutting down Redis command runner")
			return

		case payload := <-m.commandCh:
			switch payload.name {
			case StartCommand:
				go m.watchStatus()

			case StopCommand:
				m.logger.Infof("Stop command sent to Redis")
				m.cleanup()

			case RunAsMasterCommand:
				if !m.state.Ready {
					m.logger.Warnf("Got 'runAsMaster' command, but Redis is not ready yet - ignoring")
					continue
				}

				if err := m.runAsMaster(); err != nil {
					m.logger.Error(err)
				}

			case RunAsSlaveCommand:
				if !m.state.Ready {
					m.logger.Warnf("Got 'runAsSlave' command, but Redis is not ready yet - ignoring")
					continue
				}

				if err := m.runAsSlave(payload.consulState.MasterAddr, payload.consulState.MasterPort); err != nil {
					m.logger.Error(err)
					continue
				}

				if err := m.disconnectUsers(); err != nil {
					m.logger.Error(err)
				}
			}
		}
	}
}

// watchStatus checks redis replication status
func (m *Manager) watchStatus() {
	if m.watcherRunning {
		m.logger.Warn("Trying to start status watcher, but already running, ignoring...")
		return
	}
	m.watcherRunning = true

	defer func() {
		m.watcherRunning = false
	}()

	interval := time.Second
	timer := time.NewTimer(interval)

	for {
		select {
		case <-timer.C:
			result, err := m.client.Info().Result()
			// any failure will trigger a disconnect event
			if err != nil {
				m.state.Healthy = false
				m.emit()

				// Lets start backing off in case it's a long standing issue that is going on
				backoffDuration := m.backoff.Duration()
				m.logger.Errorf("Redis is not healthy, going to apply backoff of %s until next attempt: %s", backoffDuration.Round(time.Second).String(), err)
				timer.Reset(backoffDuration)
				continue
			}

			// Success, reset the backoff counter
			m.backoff.Reset()

			// Queue next execution
			timer.Reset(interval)

			// Parse the "info" result from Redis
			info := m.parseInfoResult(result)

			// If Redis is loading data from disk, do not mark us as healthy
			// If Redis is _not_ loading data from disk, we're healthy
			m.state.Healthy = !info.Loading

			// Mark Redis as "ready" (e.g. we can connect)
			m.state.Ready = true

			// Update state with most recent output
			m.state.Info = info
			m.state.InfoString = result
			m.emit()
		}
	}
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

func (m *Manager) parseInfoResult(str string) state.RedisStatus {
	kvPair := m.parseKeyValue(str)

	// Create new replication state
	newState := state.RedisStatus{
		Role: kvPair["role"],
	}

	// Add master_host to state (if set) - only available for slaves
	if masterHost, ok := kvPair["master_host"]; ok {
		newState.MasterHost = masterHost
	}

	// Add master_port to state (if set) - only available for slaves
	if masterPortString, ok := kvPair["master_port"]; ok {
		masterPort, err := strconv.Atoi(masterPortString)
		if err == nil {
			newState.MasterPort = masterPort
		}
	}

	// Is the master link up?
	if masterLinkUpString, ok := kvPair["master_link_status"]; ok {
		newState.MasterLinkUp = masterLinkUpString == "up"
	}

	// Is master sync in process?
	if masterSyncInProgress, ok := kvPair["master_sync_in_progress"]; ok {
		newState.MasterSyncInProgress = masterSyncInProgress != "0"
	}

	// track the time since the master link went down
	if masterLinkDownSinceSecondsString, ok := kvPair["master_link_down_since_seconds"]; ok {
		number, _ := strconv.Atoi(masterLinkDownSinceSecondsString)
		newState.MasterLinkDownSince = time.Duration(number) * time.Second
	}

	// track if redis is loading data
	if loadingStr, ok := kvPair["loading"]; ok {
		loading, _ := strconv.ParseBool(loadingStr)
		newState.Loading = loading
	}

	return newState
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
