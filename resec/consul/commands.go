package consul

import "github.com/seatgeek/resec/resec/state"

// list of commands the Consul connection can handle
const (
	StartCommand             = CommandName("start")
	StopConsulCommand        = CommandName("stop")
	RegisterServiceCommand   = CommandName("register_service")
	DeregisterServiceCommand = CommandName("deregister_service")
	UpdateServiceCommand     = CommandName("update_service")
	ReleaseLockCommand       = CommandName("release_lock")
)

// CommandName is a special string type
type CommandName string

// Command is the payload sent to the Consul connection
// it consist of the commad name and the redis state
type Command struct {
	name       CommandName
	redisState state.Redis
}

// Name will return the Command name
func (c *Command) Name() CommandName {
	return c.name
}

// String will return the Command name as a string
func (c *Command) String() string {
	return string(c.name)
}

// NewCommand will create a Command object
func NewCommand(cmd CommandName, redisState state.Redis) Command {
	return Command{
		name:       cmd,
		redisState: redisState,
	}
}
