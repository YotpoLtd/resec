package redis

import "github.com/YotpoLtd/resec/resec/state"

const (
	StartCommand       = commandType("start")
	StopCommand        = commandType("stop")
	RunAsSlaveCommand  = commandType("run_as_slave")
	RunAsMasterCommand = commandType("run_as_master")
)

type commandType string

type Command struct {
	command     commandType
	consulState state.Consul
}

func NewCommand(cmd commandType, consulState state.Consul) Command {
	return Command{
		command:     cmd,
		consulState: consulState,
	}
}
