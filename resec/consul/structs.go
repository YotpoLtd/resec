package consul

import (
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/jpillora/backoff"
	"github.com/seatgeek/resec/resec/state"
	log "github.com/sirupsen/logrus"
)

type Manager struct {
	backoff      *backoff.Backoff  // exponential backoff helper
	client       consulClient      // consul API client
	clientConfig *consulapi.Config // consul API client configuration
	config       *config           // configuration for this connection
	lockCh       <-chan struct{}   // lock channel used by Consul SDK to notify about changes
	lockErrorCh  <-chan struct{}   // lock error channel used by Consul SDK to notify about errors related to the lock
	logger       *log.Entry        // logger for the consul connection struct
	state        *state.Consul     // state used by the reconciler
	stateCh      chan state.Consul // state channel used to notify the reconciler of changes
	stopCh       chan interface{}  // internal channel used to stop all go-routines when gracefully shutting down
	commandCh    chan Command
}

// Consul config used for internal state management
type config struct {
	announceAddr             string              // address (IP:port) to announce to Consul
	announceHost             string              // host (IP) to announce to Consul
	announcePort             int                 // port to announce to Consul
	checkID                  string              // consul check ID
	deregisterServiceAfter   time.Duration       // after how long time in warnign should service be auto-deregistered
	lock                     *consulapi.Lock     // Consul Lock instance
	lockKey                  string              // Consul KV key to lock
	lockMonitorRetries       int                 // How many times should we retry to reclaim the lock if we lose connectivity to Consul / leader election
	lockMonitorRetryInterval time.Duration       // How long to wait between retries
	lockSessionName          string              // Name of the session that we use to claim the lock
	voluntarilyReleaseLockCh chan interface{}    // Release the lock if this channel is closed (if we have it, otherwise NOOP)
	lockTTL                  time.Duration       // How frequently do we ned to keep-alive our lock to have it not expire
	serviceID                string              // Consul service ID
	serviceName              string              // Consul service Name
	serviceNamePrefix        string              // Consul service name (prefix)
	serviceTagsByRole        map[string][]string // Tags to include for a service, depending on the role we have
	serviceTTL               time.Duration
}
