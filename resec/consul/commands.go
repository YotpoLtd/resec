package consul

import "github.com/YotpoLtd/resec/resec/state"

const (
	StartCommand             = commandName("start")
	StopConsulCommand        = commandName("stop")
	RegisterServiceCommand   = commandName("register_service")
	DeregisterServiceCommand = commandName("deregister_service")
	UpdateServiceCommand     = commandName("update_service")
	ReleaseLockCommand       = commandName("release_lock")
)

type commandName string

type Command struct {
	name       commandName
	redisState state.Redis
}

func NewCommand(cmd commandName, redisState state.Redis) Command {
	return Command{
		name:       cmd,
		redisState: redisState,
	}
}
