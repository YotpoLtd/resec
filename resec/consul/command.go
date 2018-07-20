package consul

import "github.com/YotpoLtd/resec/resec/state"

const (
	StartCommand             = commandType("start")
	StopConsulCommand        = commandType("stop")
	RegisterServiceCommand   = commandType("register_service")
	DeregisterServiceCommand = commandType("deregister_service")
	UpdateServiceCommand     = commandType("update_service")
	ReleaseLockCommand       = commandType("release_lock")
)

type commandType string

type Command struct {
	kind       commandType
	redisState state.Redis
}

func NewCommand(cmd commandType, redisState state.Redis) Command {
	return Command{
		kind:       cmd,
		redisState: redisState,
	}
}
