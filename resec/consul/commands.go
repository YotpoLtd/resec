package consul

import "github.com/YotpoLtd/resec/resec/state"

const (
	StartCommand             = CommandName("start")
	StopConsulCommand        = CommandName("stop")
	RegisterServiceCommand   = CommandName("register_service")
	DeregisterServiceCommand = CommandName("deregister_service")
	UpdateServiceCommand     = CommandName("update_service")
	ReleaseLockCommand       = CommandName("release_lock")
)

type CommandName string

type Command struct {
	name       CommandName
	redisState state.Redis
}

func (c *Command) Name() CommandName {
	return c.name
}

func (c *Command) String() string {
	return string(c.name)
}

func NewCommand(cmd CommandName, redisState state.Redis) Command {
	return Command{
		name:       cmd,
		redisState: redisState,
	}
}
